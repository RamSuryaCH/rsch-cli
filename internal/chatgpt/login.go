package chatgpt

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// LoginOptions control an interactive sign-in.
type LoginOptions struct {
	// OpenBrowser opens the authorization URL. When it fails, the URL (with
	// id_token_hint removed) is passed to ShowURL instead.
	OpenBrowser func(string) error
	ShowURL     func(string)
	// Port is the loopback port to try first; 0 means PreferredPort. If it
	// is busy another free port is used, as the docs allow.
	Port    int
	Timeout time.Duration
	// NewAccount forces a new registration even when one is saved.
	NewAccount bool
}

type callbackResult struct {
	code, state, clientID, scope, err, errDescription string
}

// Login runs the browser sign-in and saves the verified connection.
func (c *Client) Login(ctx context.Context, opts LoginOptions) (Record, error) {
	hostID, err := c.Store.HostID()
	if err != nil {
		return Record{}, err
	}
	saved, hasSaved, err := c.Store.Load()
	if err != nil {
		return Record{}, err
	}
	discovery, err := c.Discover(ctx)
	if err != nil {
		return Record{}, err
	}

	reauth := hasSaved && saved.ClientID != "" && !opts.NewAccount
	request := AuthorizeRequest{ClientID: DynamicClientID, HostID: hostID}
	if reauth {
		request.ClientID = saved.ClientID
		request.IDTokenHint = saved.IDToken
		request.LoginHint = saved.Email
	}
	pkce, err := NewPKCE()
	if err != nil {
		return Record{}, err
	}
	if request.State, err = randomToken(24); err != nil {
		return Record{}, err
	}
	if request.Nonce, err = randomToken(24); err != nil {
		return Record{}, err
	}
	request.Challenge = pkce.Challenge

	listener, err := listenLoopback(opts.Port)
	if err != nil {
		return Record{}, err
	}
	request.RedirectURI = "http://127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + CallbackPath
	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	authorizeURL := AuthorizeURL(discovery.AuthorizationEndpoint, request)
	if opts.OpenBrowser == nil || opts.OpenBrowser(authorizeURL) != nil {
		if opts.ShowURL != nil {
			opts.ShowURL(RedactURL(authorizeURL))
		}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	var cb callbackResult
	select {
	case cb = <-results:
	case <-time.After(timeout):
		return Record{}, errors.New("timed out waiting for the browser sign-in")
	case <-ctx.Done():
		return Record{}, ctx.Err()
	}

	// Validate state before trusting anything else in the callback.
	if cb.state != request.State {
		return Record{}, errors.New("sign-in callback state does not match; aborted")
	}
	if cb.err != "" {
		if cb.err == "access_denied" {
			return Record{}, errors.New("sign-in was cancelled or ChatGPT plan use was not allowed (access_denied)")
		}
		return Record{}, fmt.Errorf("sign-in failed: %s %s", cb.err, cb.errDescription)
	}
	if cb.code == "" {
		return Record{}, errors.New("sign-in callback has no authorization code")
	}
	clientID := request.ClientID
	if reauth {
		if cb.clientID != "" && cb.clientID != saved.ClientID {
			return Record{}, errors.New("sign-in returned a different client ID than the saved registration; rejected")
		}
	} else {
		if cb.clientID == "" || cb.clientID == DynamicClientID {
			return Record{}, errors.New("registration incomplete: the callback did not include an issued client ID")
		}
		clientID = cb.clientID
		// Save the issued client ID before the code exchange, so a failed or
		// expired code does not lead to registering another client.
		pending := Record{ClientID: clientID, ExtAgentHostID: hostID}
		if err := c.Store.Save(pending); err != nil {
			return Record{}, fmt.Errorf("save issued client ID: %w", err)
		}
	}

	tokens, err := c.tokenRequest(ctx, discovery.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {cb.code},
		"code_verifier": {pkce.Verifier},
		"redirect_uri":  {request.RedirectURI},
		"resource":      {Resource},
	})
	if err != nil {
		return Record{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	identity, err := c.VerifyIDToken(ctx, discovery, tokens.IDToken, clientID, request.Nonce)
	if err != nil {
		return Record{}, err
	}
	if reauth && saved.Subject != "" && (identity.Subject != saved.Subject || identity.Issuer != saved.Issuer) {
		return Record{}, errors.New("signed in to a different ChatGPT account than the saved one; use --new-account to add it")
	}

	record := Record{
		Email:          identity.Email,
		Issuer:         identity.Issuer,
		Subject:        identity.Subject,
		ClientID:       clientID,
		ExtAgentHostID: hostID,
		IDToken:        tokens.IDToken,
		AccessToken:    tokens.AccessToken,
		RefreshToken:   tokens.RefreshToken,
		TokenType:      tokens.TokenType,
		ExpiresIn:      tokens.ExpiresIn,
		// Plan usage is decided from the token response's granted scopes.
		Scopes:  splitScopes(tokens.Scope),
		SavedAt: c.now().UTC(),
	}
	if err := c.Store.Save(record); err != nil {
		return Record{}, fmt.Errorf("save credentials: %w", err)
	}
	return record, nil
}

func listenLoopback(port int) (net.Listener, error) {
	if port == 0 {
		port = PreferredPort
	}
	if port < 0 {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		return l, nil
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func callbackHandler(results chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result := callbackResult{
			code: q.Get("code"), state: q.Get("state"), clientID: q.Get("client_id"),
			scope: q.Get("scope"), err: q.Get("error"), errDescription: q.Get("error_description"),
		}
		message := "Signed in. You can close this tab and return to RSCH."
		if result.err != "" {
			message = "Sign-in did not complete: " + result.err + ". You can close this tab."
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>RSCH</title><p>%s</p>", html.EscapeString(message))
		select {
		case results <- result:
		default:
		}
	})
	return mux
}
