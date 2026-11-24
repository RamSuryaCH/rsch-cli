// Package evidence stores inspectable, redacted observations produced by
// RSCH analysis and verification commands. Producers write here once; the
// CLI, HTTP service, and workflow clients can then consume the same records.
package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/security"
)

type Status string

const (
	Observed    Status = "observed"
	Unavailable Status = "unavailable"
	Failed      Status = "failed"
)

// Record is intentionally data-oriented. Data is JSON so new producer
// schemas can be added without coupling this package to every subsystem.
type Record struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Status     Status          `json:"status"`
	Source     string          `json:"source"`
	CapturedAt time.Time       `json:"capturedAt"`
	Summary    string          `json:"summary,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// Link attaches an immutable evidence record to a persisted workflow object.
// Keeping links separate from records preserves the append-only observation
// log while allowing a task, requirement, or goal to gain traceability later.
type Link struct {
	ID            string    `json:"id"`
	EvidenceID    string    `json:"evidenceId"`
	GoalID        string    `json:"goalId,omitempty"`
	TaskID        string    `json:"taskId,omitempty"`
	RequirementID string    `json:"requirementId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

type LinkQuery struct {
	EvidenceID    string
	GoalID        string
	TaskID        string
	RequirementID string
}

type Store struct {
	root      string
	linksRoot string
	mu        sync.Mutex
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("evidence root is required")
	}
	dir := filepath.Join(root, ".rsch", "evidence")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	linksRoot := filepath.Join(root, ".rsch", "evidence-links")
	if err := os.MkdirAll(linksRoot, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: dir, linksRoot: linksRoot}, nil
}

func (s *Store) Record(value Record) (Record, error) {
	if s == nil {
		return Record{}, errors.New("evidence store is required")
	}
	value.Kind = strings.TrimSpace(value.Kind)
	value.Source = strings.TrimSpace(value.Source)
	if value.Kind == "" || value.Source == "" {
		return Record{}, errors.New("evidence kind and source are required")
	}
	if !validPart(value.Kind) {
		return Record{}, errors.New("evidence kind contains unsafe characters")
	}
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if !validPart(value.ID) {
		return Record{}, errors.New("evidence id contains unsafe characters")
	}
	if value.CapturedAt.IsZero() {
		value.CapturedAt = time.Now().UTC()
	}
	if value.Status == "" {
		value.Status = Observed
	}
	value.Summary = security.RedactText(value.Summary)
	value.Reason = security.RedactText(value.Reason)
	if len(value.Data) > 0 {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(value.Data))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return Record{}, fmt.Errorf("evidence data must be JSON: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return Record{}, errors.New("evidence data must contain a single JSON value")
		}
		redacted, err := json.Marshal(redactJSON(decoded, ""))
		if err != nil {
			return Record{}, err
		}
		value.Data = redacted
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Record{}, err
	}
	// Check legacy timestamp-prefixed records as well as the current ID-based
	// layout. A record's identity must remain unique across kinds and dates.
	if err := ensureNewID(s.root, value.ID); err != nil {
		return Record{}, err
	}
	path := filepath.Join(s.root, value.ID+".json")
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(s.root, ".evidence-*.tmp")
	if err != nil {
		return Record{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Record{}, err
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		_ = tmp.Close()
		return Record{}, err
	}
	if err := tmp.Close(); err != nil {
		return Record{}, err
	}
	// Linking publishes the complete file atomically without replacing an
	// existing observation, including writes from another Store or process.
	if err := os.Link(tmpPath, path); err != nil {
		return Record{}, err
	}
	return value, nil
}

func (s *Store) List(limit int) ([]Record, error) {
	if s == nil {
		return nil, errors.New("evidence store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	items := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item Record
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("read evidence %s: %w", entry.Name(), err)
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CapturedAt.Equal(items[j].CapturedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CapturedAt.Before(items[j].CapturedAt)
	})
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

func (s *Store) Get(id string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("evidence store is required")
	}
	if !validPart(id) {
		return Record{}, errors.New("evidence id is invalid")
	}
	items, err := s.List(0)
	if err != nil {
		return Record{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return Record{}, os.ErrNotExist
}

// CreateLink appends a relationship without modifying the original evidence.
// At least one workflow reference is required, and all references are path
// safe so the link store cannot be used for traversal.
func (s *Store) CreateLink(link Link) (Link, error) {
	if s == nil {
		return Link{}, errors.New("evidence store is required")
	}
	if !validPart(link.EvidenceID) {
		return Link{}, errors.New("evidence id is required")
	}
	if strings.TrimSpace(link.GoalID) == "" && strings.TrimSpace(link.TaskID) == "" && strings.TrimSpace(link.RequirementID) == "" {
		return Link{}, errors.New("at least one workflow reference is required")
	}
	for _, value := range []string{link.GoalID, link.TaskID, link.RequirementID} {
		if value != "" && !validPart(value) {
			return Link{}, errors.New("workflow reference contains unsafe characters")
		}
	}
	if _, err := s.Get(link.EvidenceID); err != nil {
		return Link{}, fmt.Errorf("link evidence: %w", err)
	}
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	if !validPart(link.ID) {
		return Link{}, errors.New("link id contains unsafe characters")
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	encoded, err := json.MarshalIndent(link, "", "  ")
	if err != nil {
		return Link{}, err
	}
	if err := ensureNewID(s.linksRoot, link.ID); err != nil {
		return Link{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp(s.linksRoot, ".link-*.tmp")
	if err != nil {
		return Link{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Link{}, err
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		_ = tmp.Close()
		return Link{}, err
	}
	if err := tmp.Close(); err != nil {
		return Link{}, err
	}
	path := filepath.Join(s.linksRoot, link.ID+".json")
	if err := os.Link(tmpPath, path); err != nil {
		return Link{}, err
	}
	return link, nil
}

func (s *Store) ListLinks(query LinkQuery, limit int) ([]Link, error) {
	if s == nil {
		return nil, errors.New("evidence store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.linksRoot)
	if err != nil {
		return nil, err
	}
	items := make([]Link, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.linksRoot, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item Link
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("read evidence link %s: %w", entry.Name(), err)
		}
		if query.EvidenceID != "" && item.EvidenceID != query.EvidenceID || query.GoalID != "" && item.GoalID != query.GoalID || query.TaskID != "" && item.TaskID != query.TaskID || query.RequirementID != "" && item.RequirementID != query.RequirementID {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

func validPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// ensureNewID checks legacy timestamp-prefixed filenames without reading every
// observation. The final os.Link still arbitrates concurrent current writers.
func ensureNewID(root, id string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == id+".json" {
			return fmt.Errorf("evidence identity %q: %w", id, os.ErrExist)
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "-"+id+".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &identity); err != nil {
			return fmt.Errorf("read evidence identity %s: %w", entry.Name(), err)
		}
		if identity.ID == id {
			return fmt.Errorf("evidence identity %q: %w", id, os.ErrExist)
		}
	}
	return nil
}

func redactJSON(value any, key string) any {
	lower := strings.ToLower(key)
	if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "private") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
		return "[REDACTED]"
	}
	switch item := value.(type) {
	case string:
		return security.RedactText(item)
	case []any:
		out := make([]any, len(item))
		for i, child := range item {
			out[i] = redactJSON(child, "")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(item))
		for childKey, child := range item {
			out[childKey] = redactJSON(child, childKey)
		}
		return out
	default:
		return value
	}
}
