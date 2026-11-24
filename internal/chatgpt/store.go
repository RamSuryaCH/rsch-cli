package chatgpt

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	hostFileName = "chatgpt-host.json"
	authFileName = "chatgpt-auth.json"
	lockFileName = "chatgpt-auth.lock"

	// DirEnv overrides where credentials are stored (used by tests).
	DirEnv = "RSCH_CHATGPT_DIR"
)

// Record is the saved connection for one ChatGPT account registration. The
// field names follow OpenAI's documented credential record.
type Record struct {
	Email          string    `json:"email,omitempty"`
	Issuer         string    `json:"issuer,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	ClientID       string    `json:"client_id"`
	ExtAgentHostID string    `json:"ext_agent_host_id"`
	IDToken        string    `json:"id_token,omitempty"`
	AccessToken    string    `json:"access_token,omitempty"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	TokenType      string    `json:"token_type,omitempty"`
	ExpiresIn      int64     `json:"expires_in,omitempty"`
	Scopes         []string  `json:"scopes,omitempty"`
	SavedAt        time.Time `json:"saved_at,omitempty"`
}

// ExpiresAt is when the access token expires, or zero when unknown.
func (r Record) ExpiresAt() time.Time {
	if r.ExpiresIn <= 0 || r.SavedAt.IsZero() {
		return time.Time{}
	}
	return r.SavedAt.Add(time.Duration(r.ExpiresIn) * time.Second)
}

// HasScope reports whether scope was granted.
func (r Record) HasScope(scope string) bool {
	for _, s := range r.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Connected reports whether the record holds usable credentials.
func (r Record) Connected() bool { return r.ClientID != "" && r.AccessToken != "" }

// PlanUsage reports whether requests may use the user's ChatGPT plan: the
// account is connected and chatgpt.tokens.use.direct was actually granted.
// A verified sign-in alone is not enough.
func (r Record) PlanUsage() bool { return r.Connected() && r.HasScope(ScopePlanUsage) }

// Store reads and writes the host ID and credential record.
type Store struct{ Dir string }

// DefaultStore uses $RSCH_CHATGPT_DIR, or the RSCH directory in the user's
// config directory (~/.config/rsch on Linux, ~/Library/Application
// Support/rsch on macOS).
func DefaultStore() (Store, error) {
	if dir := strings.TrimSpace(os.Getenv(DirEnv)); dir != "" {
		return Store{Dir: dir}, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return Store{}, fmt.Errorf("locate config directory: %w", err)
	}
	return Store{Dir: filepath.Join(base, "rsch")}, nil
}

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// HostID returns this installation's ext_agent_host_id, creating and saving
// a random urn:uuid on first use. It is reused across sign-ins, accounts and
// logouts.
func (s Store) HostID() (string, error) {
	var saved struct {
		HostID string `json:"ext_agent_host_id"`
	}
	data, err := os.ReadFile(s.path(hostFileName))
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &saved); jsonErr != nil {
			return "", fmt.Errorf("read %s: %w", hostFileName, jsonErr)
		}
		if strings.HasPrefix(saved.HostID, "urn:uuid:") {
			return saved.HostID, nil
		}
		return "", fmt.Errorf("%s has no valid ext_agent_host_id", s.path(hostFileName))
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	id, err := newUUIDURN()
	if err != nil {
		return "", err
	}
	saved.HostID = id
	if err := s.writeJSON(hostFileName, saved); err != nil {
		return "", err
	}
	return id, nil
}

// Load returns the saved record, or ok=false when there is none.
func (s Store) Load() (Record, bool, error) {
	data, err := os.ReadFile(s.path(authFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, false, fmt.Errorf("read %s: %w", authFileName, err)
	}
	return record, true, nil
}

// Save writes the record atomically with 0600 permissions.
func (s Store) Save(record Record) error { return s.writeJSON(authFileName, record) }

func (s Store) writeJSON(name string, value any) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

func newUUIDURN() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
