package chatgpt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeOpenAI is an in-memory OAuth server and API that follows the
// documented Sign in with ChatGPT contract closely enough to exercise
// RSCH's client. It records what it receives so tests can assert on it.
type fakeOpenAI struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	mu sync.Mutex
	// behaviour
	grantedScope    string // scope returned by the token endpoint
	callbackClient  string // client_id in the registration callback ("" = issue one)
	omitCallbackID  bool
	denyAccess      bool
	badState        bool
	failExchange    bool
	tamper          string // "aud", "nonce", "iss", "sig", "exp"
	idSubject       string
	refreshError    string
	revokeStatus    int
	responsesEvents []string
	responsesStatus int
	responsesBody   string
	expiresIn       int64
	// recorded
	authorizeQueries []url.Values
	tokenForms       []url.Values
	revokeForms      []url.Values
	apiAuth          []string
	responseBodies   []map[string]any
	pending          map[string]pendingCode
	issuedClient     string
	refreshCount     int
}

type pendingCode struct {
	clientID, challenge, redirect, nonce string
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOpenAI{
		t: t, key: key, kid: "test-key",
		grantedScope: "chatgpt.tokens.use.direct email offline_access openid profile resource.invoke",
		idSubject:    "user-123", revokeStatus: http.StatusOK, expiresIn: 3600,
		pending: map[string]pendingCode{}, issuedClient: "oaiapp_test_issued",
		responsesStatus: http.StatusOK,
	}
	f.responsesEvents = []string{
		`{"type":"response.created"}`,
		`{"type":"response.output_text.delta","delta":"Token sharing "}`,
		`{"type":"response.output_text.delta","delta":"works."}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":4}}}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/.well-known/jwks.json", f.jwks)
	mux.HandleFunc("/api/accounts/authorize", f.authorize)
	mux.HandleFunc("/api/accounts/oauth/token", f.token)
	mux.HandleFunc("/oauth/revoke", f.revoke)
	mux.HandleFunc("/v1/models", f.models)
	mux.HandleFunc("/v1/responses", f.responses)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenAI) client(t *testing.T) *Client {
	t.Helper()
	return &Client{
		Endpoints: Endpoints{Issuer: f.server.URL, APIBase: f.server.URL + "/v1"},
		HTTP:      f.server.Client(),
		Store:     Store{Dir: filepath.Join(t.TempDir(), "rsch")},
		Now:       time.Now,
	}
}

// browser simulates the user's browser: it follows the authorization
// request (where the fake server "approves" it) to the loopback callback.
func (f *fakeOpenAI) browser(authorizeURL string) error {
	go func() {
		resp, err := http.Get(authorizeURL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	return nil
}

func (f *fakeOpenAI) discovery(w http.ResponseWriter, _ *http.Request) {
	base := f.server.URL
	_ = json.NewEncoder(w).Encode(map[string]string{
		"issuer": base, "authorization_endpoint": base + "/api/accounts/authorize",
		"token_endpoint": base + "/api/accounts/oauth/token", "jwks_uri": base + "/.well-known/jwks.json",
		"revocation_endpoint": base + "/oauth/revoke",
	})
}

func (f *fakeOpenAI) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": f.kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (f *fakeOpenAI) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.authorizeQueries = append(f.authorizeQueries, q)
	f.mu.Unlock()
	callback, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || callback.Hostname() != "127.0.0.1" || callback.Path != CallbackPath {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	values := url.Values{"state": {q.Get("state")}}
	if f.badState {
		values.Set("state", "forged")
	}
	if f.denyAccess {
		values.Set("error", "access_denied")
	} else {
		clientID := q.Get("client_id")
		if clientID == DynamicClientID {
			clientID = f.issuedClient
		}
		if f.callbackClient != "" {
			clientID = f.callbackClient
		}
		code := fmt.Sprintf("code-%d", len(f.authorizeQueries))
		f.mu.Lock()
		f.pending[code] = pendingCode{clientID: clientID, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), nonce: q.Get("nonce")}
		f.mu.Unlock()
		values.Set("code", code)
		values.Set("scope", f.grantedScope)
		if !f.omitCallbackID {
			values.Set("client_id", clientID)
		}
	}
	callback.RawQuery = values.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (f *fakeOpenAI) idToken(clientID, nonce string) string {
	claims := jwt.MapClaims{
		"iss": f.server.URL, "aud": clientID, "sub": f.idSubject, "email": "surya@example.com",
		"nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	key := f.key
	switch f.tamper {
	case "aud":
		claims["aud"] = "someone-else"
	case "nonce":
		claims["nonce"] = "wrong-nonce"
	case "iss":
		claims["iss"] = "https://evil.example"
	case "exp":
		claims["exp"] = time.Now().Add(-time.Hour).Unix()
	case "sig":
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = f.kid
	signed, err := token.SignedString(key)
	if err != nil {
		f.t.Fatal(err)
	}
	return signed
}

func (f *fakeOpenAI) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := r.PostForm
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenForms = append(f.tokenForms, form)
	fail := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if form.Get("resource") != Resource {
		fail("invalid_target")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		if f.failExchange {
			fail("invalid_grant")
			return
		}
		p, ok := f.pending[form.Get("code")]
		delete(f.pending, form.Get("code"))
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || p.clientID != form.Get("client_id") || p.redirect != form.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge || form.Get("client_id") == DynamicClientID {
			fail("invalid_grant")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "id_token": f.idToken(p.clientID, p.nonce),
			"token_type": "Bearer", "expires_in": f.expiresIn, "scope": f.grantedScope,
		})
	case "refresh_token":
		if f.refreshError != "" {
			fail(f.refreshError)
			return
		}
		f.refreshCount++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("access-%d", f.refreshCount+1), "refresh_token": fmt.Sprintf("refresh-%d", f.refreshCount+1),
			"token_type": "Bearer", "expires_in": 3600,
		})
	default:
		fail("unsupported_grant_type")
	}
}

func (f *fakeOpenAI) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.revokeForms = append(f.revokeForms, r.PostForm)
	status := f.revokeStatus
	f.mu.Unlock()
	w.WriteHeader(status)
}

func (f *fakeOpenAI) models(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-test","display_name":"GPT Test","visibility":"list"},{"slug":"hidden","display_name":"Hidden","visibility":"hide"},{"slug":"gpt-mini","display_name":"GPT Mini","visibility":"list"}]}`))
}

func (f *fakeOpenAI) responses(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
	f.responseBodies = append(f.responseBodies, body)
	status, errBody, events := f.responsesStatus, f.responsesBody, f.responsesEvents
	f.mu.Unlock()
	if status != http.StatusOK {
		w.Header().Set("x-request-id", "req_123")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(errBody))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var typed struct{ Type string }
		_ = json.Unmarshal([]byte(e), &typed)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typed.Type, e)
	}
}

func signIn(t *testing.T, f *fakeOpenAI, c *Client, opts LoginOptions) (Record, error) {
	t.Helper()
	opts.OpenBrowser = f.browser
	opts.Port = -1 // forces a random free port in tests
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	return c.Login(t.Context(), opts)
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q does not contain %q", haystack, needle)
	}
}
