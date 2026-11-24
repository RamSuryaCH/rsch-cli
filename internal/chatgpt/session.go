package chatgpt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotSignedIn means there is no usable ChatGPT connection.
var ErrNotSignedIn = errors.New("not signed in to ChatGPT; run: rsch login chatgpt")

// ErrPlanUsageNotGranted means the connection lacks chatgpt.tokens.use.direct.
var ErrPlanUsageNotGranted = errors.New("ChatGPT plan usage was not granted for RSCH; run rsch login chatgpt again and allow it, or manage access at " + ManageUsageURL)

// refreshWindow is how close to expiry a token is refreshed.
const refreshWindow = 2 * time.Minute

// terminalRefreshErrors mean the refresh token can no longer be used.
var terminalRefreshErrors = map[string]bool{
	"invalid_grant": true, "invalid_refresh_token": true, "token_expired": true,
	"refresh_token_expired": true, "refresh_token_invalidated": true, "refresh_token_reused": true,
}

// AccessToken returns a valid access token for plan usage, refreshing it
// when it is close to expiry.
func (c *Client) AccessToken(ctx context.Context) (string, Record, error) {
	record, ok, err := c.Store.Load()
	if err != nil {
		return "", record, err
	}
	if !ok || !record.Connected() {
		return "", record, ErrNotSignedIn
	}
	if !record.PlanUsage() {
		return "", record, ErrPlanUsageNotGranted
	}
	if expires := record.ExpiresAt(); !expires.IsZero() && c.now().Add(refreshWindow).After(expires) {
		record, err = c.Refresh(ctx)
		if err != nil {
			return "", record, err
		}
	}
	return record.AccessToken, record, nil
}

// Refresh rotates the access and refresh tokens. Refreshes are serialized
// across processes, and a token refreshed by another process is reused.
func (c *Client) Refresh(ctx context.Context) (Record, error) {
	unlock, err := c.Store.lock()
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record, ok, err := c.Store.Load()
	if err != nil {
		return record, err
	}
	if !ok || record.RefreshToken == "" {
		return record, ErrNotSignedIn
	}
	if expires := record.ExpiresAt(); !expires.IsZero() && c.now().Add(refreshWindow).Before(expires) {
		return record, nil // another process already refreshed
	}
	discovery, err := c.Discover(ctx)
	if err != nil {
		return record, err
	}
	tokens, err := c.tokenRequest(ctx, discovery.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {record.ClientID},
		"refresh_token": {record.RefreshToken},
		"resource":      {Resource},
	})
	if err != nil {
		var oauthErr *OAuthError
		if errors.As(err, &oauthErr) && terminalRefreshErrors[oauthErr.Code] {
			record = clearTokens(record)
			if saveErr := c.Store.Save(record); saveErr != nil {
				return record, saveErr
			}
			return record, fmt.Errorf("ChatGPT session expired (%s); run: rsch login chatgpt", oauthErr.Code)
		}
		return record, fmt.Errorf("refresh ChatGPT token: %w", err)
	}
	// Replace the access token, expiry, scopes and rotating refresh token
	// together.
	record.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		record.RefreshToken = tokens.RefreshToken
	}
	if tokens.IDToken != "" {
		record.IDToken = tokens.IDToken
	}
	if tokens.TokenType != "" {
		record.TokenType = tokens.TokenType
	}
	record.ExpiresIn = tokens.ExpiresIn
	if tokens.Scope != "" {
		record.Scopes = splitScopes(tokens.Scope)
	}
	record.SavedAt = c.now().UTC()
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	return record, nil
}

// LogoutResult reports what logout did.
type LogoutResult struct {
	Revoked bool
	// Warning is set when remote revocation was not confirmed.
	Warning string
}

// Logout revokes the refresh token and clears tokens locally. The issued
// client ID, account identity and host ID are kept for a later sign-in.
func (c *Client) Logout(ctx context.Context) (LogoutResult, error) {
	record, ok, err := c.Store.Load()
	if err != nil {
		return LogoutResult{}, err
	}
	if !ok || (record.AccessToken == "" && record.RefreshToken == "") {
		return LogoutResult{}, ErrNotSignedIn
	}
	result := LogoutResult{}
	if record.RefreshToken != "" {
		revokeErr := c.revoke(ctx, record)
		if revokeErr == nil {
			result.Revoked = true
		} else {
			result.Warning = fmt.Sprintf("remote revocation was not confirmed (%v); you can disconnect RSCH in ChatGPT Settings", revokeErr)
		}
	}
	if err := c.Store.Save(clearTokens(record)); err != nil {
		return result, err
	}
	return result, nil
}

func (c *Client) revoke(ctx context.Context, record Record) error {
	discovery, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	if discovery.RevocationEndpoint == "" {
		return errors.New("the OpenID configuration has no revocation_endpoint")
	}
	form := url.Values{"token": {record.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {record.ClientID}}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.RevocationEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("revocation returned HTTP %d", resp.StatusCode)
		if resp.StatusCode < 500 {
			return lastErr
		}
	}
	return lastErr
}

func clearTokens(record Record) Record {
	record.AccessToken, record.RefreshToken, record.IDToken = "", "", ""
	record.ExpiresIn, record.Scopes = 0, nil
	return record
}
