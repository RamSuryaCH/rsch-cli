package chatgpt

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirstSignInRegistersVerifiesAndStoresPrivately(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	record, err := signIn(t, f, c, LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := f.authorizeQueries[0]
	for key, want := range map[string]string{
		"client_id": DynamicClientID, "agent_name_hint": "RSCH CLI", "response_type": "code",
		"scope":    "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
		"resource": "https://api.openai.com/v1", "code_challenge_method": "S256",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("authorize %s = %q, want %q", key, got, want)
		}
	}
	if !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize request missing host ID, state, nonce or PKCE: %v", q)
	}
	redirect, _ := url.Parse(q.Get("redirect_uri"))
	if redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Path != "/auth/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Has("id_token_hint") || q.Has("login_hint") {
		t.Fatal("first registration must not send account hints")
	}

	form := f.tokenForms[0]
	if form.Get("client_id") != "oaiapp_test_issued" || form.Get("redirect_uri") != q.Get("redirect_uri") || form.Get("code_verifier") == "" || form.Has("client_secret") {
		t.Fatalf("token exchange form = %v", form)
	}
	if record.ClientID != "oaiapp_test_issued" || record.Subject != "user-123" || record.Email != "surya@example.com" || record.Issuer != f.server.URL {
		t.Fatalf("record = %+v", record)
	}
	if !record.PlanUsage() || record.ExtAgentHostID != q.Get("ext_agent_host_id") {
		t.Fatalf("plan usage %v, host %q", record.PlanUsage(), record.ExtAgentHostID)
	}
	for _, name := range []string{authFileName, hostFileName} {
		info, err := os.Stat(filepath.Join(c.Store.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", name, perm)
		}
	}
	if info, _ := os.Stat(c.Store.Dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir permissions = %o, want 700", info.Mode().Perm())
	}
}

func TestHostIDIsStableAcrossSignIns(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	first, second := f.authorizeQueries[0].Get("ext_agent_host_id"), f.authorizeQueries[1].Get("ext_agent_host_id")
	if first == "" || first != second {
		t.Fatalf("host IDs %q and %q must match", first, second)
	}
}

func TestIssuedClientIDIsSavedBeforeCodeExchange(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	f.failExchange = true
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v, want invalid_grant", err)
	}
	saved, ok, err := c.Store.Load()
	if err != nil || !ok || saved.ClientID != "oaiapp_test_issued" || saved.AccessToken != "" {
		t.Fatalf("saved = %+v, ok %v, err %v; want the issued client ID and no tokens", saved, ok, err)
	}
	// A retry reuses the issued client ID instead of registering again.
	f.failExchange = false
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	retry := f.authorizeQueries[1]
	if retry.Get("client_id") != "oaiapp_test_issued" || retry.Has("agent_name_hint") {
		t.Fatalf("retry authorize = %v", retry)
	}
}

func TestReauthorizationUsesSavedRegistrationAndHints(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	first, err := signIn(t, f, c, LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	q := f.authorizeQueries[1]
	if q.Get("client_id") != first.ClientID || q.Has("agent_name_hint") || q.Get("id_token_hint") != first.IDToken || q.Get("login_hint") != "surya@example.com" {
		t.Fatalf("reauthorization request = %v", q)
	}
}

func TestReauthorizationRejectsDifferentClientOrAccount(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	f.callbackClient = "oaiapp_other"
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "different client ID") {
		t.Fatalf("err = %v", err)
	}
	f.callbackClient = ""
	f.idSubject = "someone-else"
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "different ChatGPT account") {
		t.Fatalf("err = %v", err)
	}
	saved, _, _ := c.Store.Load()
	if saved.Subject != "user-123" || saved.AccessToken != "access-1" {
		t.Fatalf("rejected sign-in replaced credentials: %+v", saved)
	}
}

func TestIDTokenVerificationRejectsBadTokens(t *testing.T) {
	for _, tamper := range []string{"aud", "nonce", "iss", "sig", "exp"} {
		t.Run(tamper, func(t *testing.T) {
			f := newFakeOpenAI(t)
			c := f.client(t)
			f.tamper = tamper
			if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "ID token rejected") {
				t.Fatalf("err = %v, want ID token rejected", err)
			}
			if saved, _, _ := c.Store.Load(); saved.AccessToken != "" {
				t.Fatal("tokens saved despite a rejected ID token")
			}
		})
	}
}

func TestCallbackStateAndDenialAreHandled(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	f.badState = true
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "state does not match") {
		t.Fatalf("err = %v", err)
	}
	f.badState, f.denyAccess = false, true
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
	if len(f.tokenForms) != 0 {
		t.Fatal("no code may be exchanged after a bad state or denial")
	}
	f.denyAccess, f.omitCallbackID = false, true
	if _, err := signIn(t, f, c, LoginOptions{}); err == nil || !strings.Contains(err.Error(), "registration incomplete") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanUsageNeedsTheGrantedScope(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	f.grantedScope = "openid profile email offline_access"
	record, err := signIn(t, f, c, LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if record.PlanUsage() {
		t.Fatal("plan usage enabled without chatgpt.tokens.use.direct")
	}
	if _, err := c.ListModels(t.Context()); !errors.Is(err, ErrPlanUsageNotGranted) {
		t.Fatalf("err = %v, want ErrPlanUsageNotGranted", err)
	}
	if len(f.apiAuth) != 0 {
		t.Fatal("API called without plan-usage permission")
	}
}

func TestRefreshRotatesTokensAndKeepsScopes(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	f.expiresIn = 30 // inside the refresh window
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	token, record, err := c.AccessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	form := f.tokenForms[len(f.tokenForms)-1]
	if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "oaiapp_test_issued" || form.Get("refresh_token") != "refresh-1" || form.Get("resource") != Resource || form.Has("scope") {
		t.Fatalf("refresh form = %v", form)
	}
	if token != "access-2" || record.RefreshToken != "refresh-2" || !record.PlanUsage() {
		t.Fatalf("after refresh: token %q, record %+v", token, record)
	}
	// A fresh token is not refreshed again.
	if again, _, _ := c.AccessToken(t.Context()); again != "access-2" || f.refreshCount != 1 {
		t.Fatalf("unexpected second refresh: %q, %d", again, f.refreshCount)
	}
}

func TestTerminalRefreshErrorClearsTokens(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	f.expiresIn = 30
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	f.refreshError = "refresh_token_reused"
	if _, _, err := c.AccessToken(t.Context()); err == nil || !strings.Contains(err.Error(), "rsch login chatgpt") {
		t.Fatalf("err = %v", err)
	}
	saved, _, _ := c.Store.Load()
	if saved.AccessToken != "" || saved.RefreshToken != "" || saved.ClientID != "oaiapp_test_issued" {
		t.Fatalf("after terminal refresh error: %+v", saved)
	}
}

func TestLogoutRevokesAndKeepsRegistration(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := c.Logout(t.Context())
	if err != nil || !result.Revoked {
		t.Fatalf("logout = %+v, %v", result, err)
	}
	form := f.revokeForms[0]
	if form.Get("token") != "refresh-1" || form.Get("token_type_hint") != "refresh_token" || form.Get("client_id") != "oaiapp_test_issued" {
		t.Fatalf("revocation form = %v", form)
	}
	saved, _, _ := c.Store.Load()
	if saved.AccessToken != "" || saved.RefreshToken != "" || saved.IDToken != "" || saved.ClientID == "" || saved.Subject == "" {
		t.Fatalf("after logout: %+v", saved)
	}
	// The next sign-in reuses the registration but sends no id_token_hint.
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	if q := f.authorizeQueries[1]; q.Get("client_id") != "oaiapp_test_issued" || q.Has("id_token_hint") {
		t.Fatalf("post-logout authorize = %v", q)
	}
}

func TestLogoutWarnsWhenRevocationFails(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	f.revokeStatus = http.StatusInternalServerError
	result, err := c.Logout(t.Context())
	if err != nil || result.Revoked || !strings.Contains(result.Warning, "not confirmed") {
		t.Fatalf("logout = %+v, %v", result, err)
	}
	if len(f.revokeForms) != 3 {
		t.Fatalf("revocation attempts = %d, want 3 (retry on 5xx)", len(f.revokeForms))
	}
	if saved, _, _ := c.Store.Load(); saved.RefreshToken != "" {
		t.Fatal("tokens must be cleared locally even when revocation fails")
	}
}

func TestListModelsKeepsListedModelsInOrder(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	models, err := c.ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Slug != "gpt-test" || models[1].Slug != "gpt-mini" {
		t.Fatalf("models = %+v", models)
	}
	if f.apiAuth[0] != "Bearer access-1" {
		t.Fatalf("Authorization = %q", f.apiAuth[0])
	}
}

func TestStreamResponseUsesResponsesAPIAndNeedsCompletion(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	result, err := c.StreamResponse(t.Context(), ResponseRequest{Model: "gpt-test", Input: "Reply with exactly: Token sharing works."}, func(d string) { streamed.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Token sharing works." || streamed.String() != result.Text || result.InputTokens != 12 {
		t.Fatalf("result = %+v", result)
	}
	body := f.responseBodies[0]
	if body["model"] != "gpt-test" || body["store"] != false || body["stream"] != true {
		t.Fatalf("request body = %v", body)
	}

	f.responsesEvents = []string{`{"type":"response.output_text.delta","delta":"partial"}`}
	if _, err := c.StreamResponse(t.Context(), ResponseRequest{Model: "gpt-test", Input: "hi"}, nil); err == nil || !strings.Contains(err.Error(), "without response.completed") {
		t.Fatalf("err = %v", err)
	}

	f.responsesEvents = []string{`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`}
	_, err = c.StreamResponse(t.Context(), ResponseRequest{Model: "gpt-test", Input: "hi"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "subscription_sharing_usage_limit_exceeded" {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, err.Error(), ManageUsageURL)
}

func TestStreamResponseReportsErrorsBeforeTheStream(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	if _, err := signIn(t, f, c, LoginOptions{}); err != nil {
		t.Fatal(err)
	}
	f.responsesStatus, f.responsesBody = http.StatusTooManyRequests, `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"Usage limit"}}`
	_, err := c.StreamResponse(t.Context(), ResponseRequest{Model: "gpt-test", Input: "hi"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || apiErr.RequestID != "req_123" {
		t.Fatalf("err = %v", err)
	}
	f.responsesStatus, f.responsesBody = http.StatusServiceUnavailable, `{"detail":"direct routing unavailable"}`
	_, err = c.StreamResponse(t.Context(), ResponseRequest{Model: "gpt-test", Input: "hi"}, nil)
	if !errors.As(err, &apiErr) || apiErr.Code != "" || apiErr.Message != "direct routing unavailable" {
		t.Fatalf("err = %v", err)
	}
}

func TestNotSignedIn(t *testing.T) {
	c := &Client{Store: Store{Dir: t.TempDir()}, Now: time.Now}
	if _, _, err := c.AccessToken(t.Context()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Logout(t.Context()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v", err)
	}
}

func TestRedactURLRemovesIDTokenHint(t *testing.T) {
	raw := AuthorizeURL("https://auth.example/authorize", AuthorizeRequest{ClientID: "oaiapp_x", HostID: "urn:uuid:1", IDTokenHint: "secret.jwt.value", LoginHint: "a@b.c", State: "s", Nonce: "n", Challenge: "c", RedirectURI: "http://127.0.0.1:1/auth/callback"})
	redacted := RedactURL(raw)
	if strings.Contains(redacted, "secret.jwt.value") || !strings.Contains(redacted, "login_hint=") {
		t.Fatalf("redacted = %q", redacted)
	}
}

func TestDiscoveryIssuerMustMatch(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client(t)
	c.Endpoints.Issuer = f.server.URL + "/other"
	if _, err := c.Discover(t.Context()); err == nil {
		t.Fatal("discovery with a mismatched issuer must fail")
	}
}
