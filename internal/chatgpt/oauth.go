package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Documented values for the open-source token-sharing flow.
const (
	ProductionIssuer = "https://auth.openai.com"
	ProductionAPI    = "https://api.openai.com/v1"
	// Resource is the documented resource indicator for ChatGPT plan usage.
	Resource = "https://api.openai.com/v1"
	// DynamicClientID starts first-time registration. It is never saved or
	// used for token exchange.
	DynamicClientID = "dynamic_agent_client"
	// AgentName is sent as agent_name_hint on first registration only.
	AgentName = "RSCH CLI"
	// ScopePlanUsage is the scope that permits using the ChatGPT plan.
	ScopePlanUsage = "chatgpt.tokens.use.direct"
	// Scopes requested: identity, refresh, and ChatGPT plan usage.
	Scopes = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// ManageUsageURL is ChatGPT Settings → Usage.
	ManageUsageURL = "https://chatgpt.com/settings/usage"
	// CallbackPath must match exactly; only the port may vary.
	CallbackPath = "/auth/callback"
	// PreferredPort is the documented example loopback port.
	PreferredPort = 1455
)

// Endpoints locate the auth server and API. Everything except the issuer and
// API base is read from the issuer's OpenID discovery document.
type Endpoints struct {
	Issuer  string
	APIBase string
}

// Production are OpenAI's endpoints.
var Production = Endpoints{Issuer: ProductionIssuer, APIBase: ProductionAPI}

// Discovery is the subset of the OpenID configuration RSCH uses.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// Client talks to the auth server and API.
type Client struct {
	Endpoints Endpoints
	HTTP      *http.Client
	Store     Store
	Now       func() time.Time
}

// NewClient returns a client for OpenAI's production endpoints.
func NewClient(store Store) *Client {
	return &Client{Endpoints: Production, HTTP: &http.Client{Timeout: 60 * time.Second}, Store: store, Now: time.Now}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Discover loads the OpenID configuration and requires its issuer to match.
func (c *Client) Discover(ctx context.Context) (Discovery, error) {
	issuer := strings.TrimRight(c.Endpoints.Issuer, "/")
	var d Discovery
	if err := c.getJSON(ctx, issuer+"/.well-known/openid-configuration", "", &d); err != nil {
		return d, fmt.Errorf("load OpenID configuration: %w", err)
	}
	if d.Issuer != issuer {
		return d, fmt.Errorf("OpenID configuration issuer %q does not match %q", d.Issuer, issuer)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return d, errors.New("OpenID configuration is missing an authorization, token or JWKS endpoint")
	}
	return d, nil
}

// TokenResponse is the token endpoint's reply.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// OAuthError is an error reply from the token or revocation endpoint.
type OAuthError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Description, e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
}

func (c *Client) tokenRequest(ctx context.Context, endpoint string, form url.Values) (TokenResponse, error) {
	var out TokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		oauthErr := &OAuthError{Status: resp.StatusCode}
		if json.Unmarshal(body, oauthErr) != nil || oauthErr.Code == "" {
			oauthErr.Code = "http_error"
		}
		return out, oauthErr
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return out, errors.New("token response has no access_token")
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// PKCE holds a verifier and its S256 challenge.
type PKCE struct{ Verifier, Challenge string }

// NewPKCE returns a fresh PKCE pair.
func NewPKCE() (PKCE, error) {
	verifier, err := randomToken(32)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeRequest describes one authorization attempt.
type AuthorizeRequest struct {
	ClientID    string // DynamicClientID for a new registration
	HostID      string
	RedirectURI string
	State       string
	Nonce       string
	Challenge   string
	IDTokenHint string // reauthorization only
	LoginHint   string // reauthorization only
}

// AuthorizeURL builds the documented authorization request.
func AuthorizeURL(endpoint string, r AuthorizeRequest) string {
	q := url.Values{}
	q.Set("client_id", r.ClientID)
	if r.ClientID == DynamicClientID {
		q.Set("agent_name_hint", AgentName)
	} else {
		if r.IDTokenHint != "" {
			q.Set("id_token_hint", r.IDTokenHint)
		}
		if r.LoginHint != "" {
			q.Set("login_hint", r.LoginHint)
		}
	}
	q.Set("ext_agent_host_id", r.HostID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("scope", Scopes)
	q.Set("resource", Resource)
	q.Set("state", r.State)
	q.Set("nonce", r.Nonce)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", r.Challenge)
	return endpoint + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// RedactURL removes id_token_hint so an authorization URL can be shown.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("id_token_hint") == "" {
		return raw
	}
	q.Del("id_token_hint")
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String()
}

// Identity is the verified content of an ID token.
type Identity struct {
	Issuer  string
	Subject string
	Email   string
}

type idClaims struct {
	Nonce string `json:"nonce"`
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// VerifyIDToken checks the signature against the issuer's JWKS (RS256), and
// the issuer, audience (the issued client ID), expiry and nonce.
func (c *Client) VerifyIDToken(ctx context.Context, d Discovery, idToken, clientID, nonce string) (Identity, error) {
	if idToken == "" {
		return Identity{}, errors.New("ChatGPT did not return an ID token; sign in again")
	}
	keys, err := c.fetchJWKS(ctx, d.JWKSURI)
	if err != nil {
		return Identity{}, err
	}
	claims := &idClaims{}
	_, err = jwt.ParseWithClaims(idToken, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if key, ok := keys[kid]; ok {
			return key, nil
		}
		if kid == "" && len(keys) == 1 {
			for _, key := range keys {
				return key, nil
			}
		}
		return nil, fmt.Errorf("no JWKS key for kid %q", kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(d.Issuer),
		jwt.WithAudience(clientID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(c.now),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("ID token rejected: %w", err)
	}
	if nonce == "" || claims.Nonce != nonce {
		return Identity{}, errors.New("ID token rejected: nonce does not match this sign-in attempt")
	}
	if claims.Subject == "" {
		return Identity{}, errors.New("ID token rejected: no subject")
	}
	return Identity{Issuer: claims.Issuer, Subject: claims.Subject, Email: claims.Email}, nil
}

func (c *Client) fetchJWKS(ctx context.Context, uri string) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, uri, "", &set); err != nil {
		return nil, fmt.Errorf("load JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS has no usable RSA signing keys")
	}
	return keys, nil
}

func splitScopes(scope string) []string { return strings.Fields(scope) }
