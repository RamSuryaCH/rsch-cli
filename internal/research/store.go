// Package research stores inspectable sources, evidence, and claims.
//
// Research records deliberately keep provenance separate from prose so a
// conclusion can be inspected, challenged, or removed without replaying a
// model conversation.
package research

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Source struct {
	ID          string    `json:"id"`
	URI         string    `json:"uri"`
	Title       string    `json:"title,omitempty"`
	Publisher   string    `json:"publisher,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Location    string    `json:"location,omitempty"`
	Hash        string    `json:"hash,omitempty"`
	Confidence  float64   `json:"confidence"`
	RetrievedAt time.Time `json:"retrievedAt"`
}

type Evidence struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"sourceId"`
	Quote      string    `json:"quote"`
	Location   string    `json:"location,omitempty"`
	Supports   bool      `json:"supports"`
	Confidence float64   `json:"confidence"`
	CapturedAt time.Time `json:"capturedAt"`
	Hash       string    `json:"hash,omitempty"`
}

type Claim struct {
	ID          string    `json:"id"`
	Text        string    `json:"text"`
	EvidenceIDs []string  `json:"evidenceIds,omitempty"`
	Contradicts []string  `json:"contradicts,omitempty"`
	Confidence  float64   `json:"confidence"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SearchResult struct {
	Claim   Claim    `json:"claim"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}

// Health reports whether the durable research provider can be read and its
// cross-record provenance remains internally consistent.
type Health struct {
	Available bool      `json:"available"`
	Healthy   bool      `json:"healthy"`
	Reason    string    `json:"reason,omitempty"`
	Sources   int       `json:"sources"`
	Evidence  int       `json:"evidence"`
	Claims    int       `json:"claims"`
	CheckedAt time.Time `json:"checkedAt"`
}

type snapshot struct {
	Version  int        `json:"version"`
	Sources  []Source   `json:"sources"`
	Evidence []Evidence `json:"evidence"`
	Claims   []Claim    `json:"claims"`
}

type Store struct {
	root string
	path string
	mu   sync.Mutex
}

const storeFileName = "records.json"

var errUnsupportedSnapshotVersion = errors.New("unsupported research store version")

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("research root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	store := &Store{root: absolute, path: filepath.Join(absolute, ".rsch", "research", storeFileName)}
	directory, err := store.openDirectory(true)
	if err != nil {
		return nil, err
	}
	if err := directory.Close(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) AddSource(source Source) (Source, error) {
	if s == nil {
		return Source{}, errors.New("research store is required")
	}
	source.URI = strings.TrimSpace(source.URI)
	if source.URI == "" {
		return Source{}, errors.New("source URI is required")
	}
	if parsed, err := url.Parse(source.URI); err != nil || parsed.Scheme == "" {
		return Source{}, errors.New("source URI must include a scheme")
	}
	if source.Confidence == 0 {
		source.Confidence = 0.5
	}
	if source.Confidence < 0 || source.Confidence > 1 {
		return Source{}, errors.New("source confidence must be between 0 and 1")
	}
	if source.ID == "" {
		source.ID = uuid.NewString()
	}
	if source.RetrievedAt.IsZero() {
		source.RetrievedAt = time.Now().UTC()
	}
	if source.Hash == "" {
		source.Hash = hash(source.URI + "\n" + source.Title + "\n" + source.Location)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, release, err := s.acquireFileLock()
	if err != nil {
		return Source{}, err
	}
	defer release()
	data, err := s.loadLocked(directory)
	if err != nil {
		return Source{}, err
	}
	for _, existing := range data.Sources {
		if existing.URI == source.URI && existing.Location == source.Location {
			return existing, nil
		}
	}
	data.Sources = append(data.Sources, source)
	if err := s.saveLocked(directory, data); err != nil {
		return Source{}, err
	}
	return source, nil
}

func (s *Store) AddEvidence(evidence Evidence) (Evidence, error) {
	if s == nil {
		return Evidence{}, errors.New("research store is required")
	}
	if strings.TrimSpace(evidence.SourceID) == "" || strings.TrimSpace(evidence.Quote) == "" {
		return Evidence{}, errors.New("evidence source and quote are required")
	}
	if evidence.Confidence == 0 {
		evidence.Confidence = 0.5
	}
	if evidence.Confidence < 0 || evidence.Confidence > 1 {
		return Evidence{}, errors.New("evidence confidence must be between 0 and 1")
	}
	if evidence.ID == "" {
		evidence.ID = uuid.NewString()
	}
	if evidence.CapturedAt.IsZero() {
		evidence.CapturedAt = time.Now().UTC()
	}
	if evidence.Hash == "" {
		evidence.Hash = hash(evidence.SourceID + "\n" + evidence.Location + "\n" + evidence.Quote)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, release, err := s.acquireFileLock()
	if err != nil {
		return Evidence{}, err
	}
	defer release()
	data, err := s.loadLocked(directory)
	if err != nil {
		return Evidence{}, err
	}
	if !hasSource(data.Sources, evidence.SourceID) {
		return Evidence{}, fmt.Errorf("source %q not found", evidence.SourceID)
	}
	for _, existing := range data.Evidence {
		if existing.SourceID == evidence.SourceID && existing.Quote == evidence.Quote && existing.Location == evidence.Location && existing.Supports == evidence.Supports {
			return existing, nil
		}
	}
	data.Evidence = append(data.Evidence, evidence)
	if err := s.saveLocked(directory, data); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func (s *Store) AddClaim(claim Claim) (Claim, error) {
	if s == nil {
		return Claim{}, errors.New("research store is required")
	}
	claim.Text = strings.TrimSpace(claim.Text)
	if claim.Text == "" {
		return Claim{}, errors.New("claim text is required")
	}
	if claim.Confidence == 0 {
		claim.Confidence = 0.5
	}
	if claim.Confidence < 0 || claim.Confidence > 1 {
		return Claim{}, errors.New("claim confidence must be between 0 and 1")
	}
	if claim.ID == "" {
		claim.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if claim.CreatedAt.IsZero() {
		claim.CreatedAt = now
	}
	claim.UpdatedAt = now
	if claim.Status == "" {
		claim.Status = "uncertain"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, release, err := s.acquireFileLock()
	if err != nil {
		return Claim{}, err
	}
	defer release()
	data, err := s.loadLocked(directory)
	if err != nil {
		return Claim{}, err
	}
	evidence := make(map[string]Evidence, len(data.Evidence))
	for _, item := range data.Evidence {
		evidence[item.ID] = item
	}
	supporting := 0
	for _, id := range claim.EvidenceIDs {
		item, ok := evidence[id]
		if !ok {
			return Claim{}, fmt.Errorf("evidence %q not found", id)
		}
		if item.Supports {
			supporting++
		}
	}
	if len(claim.EvidenceIDs) > 0 {
		if supporting > 0 {
			claim.Status = "supported"
		} else {
			claim.Status = "contradicted"
		}
	}
	for _, existing := range data.Claims {
		if normalize(existing.Text) == normalize(claim.Text) {
			return existing, nil
		}
	}
	claimIDs := make(map[string]struct{}, len(data.Claims))
	for _, existing := range data.Claims {
		claimIDs[existing.ID] = struct{}{}
	}
	for _, contradictedID := range claim.Contradicts {
		if _, ok := claimIDs[contradictedID]; !ok {
			return Claim{}, fmt.Errorf("claim %q not found for contradiction", contradictedID)
		}
	}
	data.Claims = append(data.Claims, claim)
	if err := s.saveLocked(directory, data); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func (s *Store) Sources() ([]Source, error) {
	if s == nil {
		return nil, errors.New("research store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readSnapshot()
	if err != nil {
		return nil, err
	}
	sort.Slice(data.Sources, func(i, j int) bool { return data.Sources[i].RetrievedAt.Before(data.Sources[j].RetrievedAt) })
	return append([]Source(nil), data.Sources...), nil
}

func (s *Store) Evidence(sourceID string) ([]Evidence, error) {
	if s == nil {
		return nil, errors.New("research store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readSnapshot()
	if err != nil {
		return nil, err
	}
	out := make([]Evidence, 0)
	for _, item := range data.Evidence {
		if sourceID == "" || item.SourceID == sourceID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) Claims() ([]Claim, error) {
	if s == nil {
		return nil, errors.New("research store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readSnapshot()
	if err != nil {
		return nil, err
	}
	sort.Slice(data.Claims, func(i, j int) bool { return data.Claims[i].UpdatedAt.Before(data.Claims[j].UpdatedAt) })
	return append([]Claim(nil), data.Claims...), nil
}

// Health performs a read-only integrity check of the store. A corrupt primary
// file can still report healthy when the validated backup is usable, because
// loadLocked deliberately provides that recovery path.
func (s *Store) Health() Health {
	status := Health{CheckedAt: time.Now().UTC()}
	if s == nil {
		status.Reason = "research store is required"
		return status
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readSnapshot()
	if err != nil {
		status.Reason = err.Error()
		return status
	}
	status.Available = true
	status.Healthy = true
	status.Sources = len(data.Sources)
	status.Evidence = len(data.Evidence)
	status.Claims = len(data.Claims)
	return status
}

func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("research search query is required")
	}
	claims, err := s.Claims()
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	results := make([]SearchResult, 0)
	for _, claim := range claims {
		score := 0.0
		matched := 0
		text := strings.ToLower(claim.Text)
		for _, term := range terms {
			if strings.Contains(text, term) {
				score += 3
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		score += claim.Confidence
		results = append(results, SearchResult{Claim: claim, Score: score, Reasons: []string{"claim text match", "confidence weighted"}})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Store) readSnapshot() (snapshot, error) {
	directory, err := s.openDirectory(false)
	if err != nil {
		return snapshot{}, err
	}
	defer directory.Close()
	return s.loadLocked(directory)
}

func (s *Store) loadLocked(directory *os.Root) (snapshot, error) {
	data, err := readStoreFile(directory, storeFileName)
	if os.IsNotExist(err) {
		data, err = readStoreFile(directory, storeFileName+".bak")
		if os.IsNotExist(err) {
			return snapshot{Version: 1, Sources: []Source{}, Evidence: []Evidence{}, Claims: []Claim{}}, nil
		}
	}
	if err != nil {
		return snapshot{}, err
	}
	out, primaryErr := decodeSnapshot(data)
	if primaryErr == nil {
		return out, nil
	}
	if errors.Is(primaryErr, errUnsupportedSnapshotVersion) {
		return snapshot{}, primaryErr
	}
	backup, backupErr := readStoreFile(directory, storeFileName+".bak")
	if backupErr == nil {
		if recovered, recoveredErr := decodeSnapshot(backup); recoveredErr == nil {
			return recovered, nil
		}
	}
	return snapshot{}, fmt.Errorf("invalid research store: %w", primaryErr)
}

func decodeSnapshot(data []byte) (snapshot, error) {
	var out snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		return snapshot{}, err
	}
	if out.Version == 0 {
		out.Version = 1
	}
	if out.Version != 1 {
		return snapshot{}, fmt.Errorf("%w %d", errUnsupportedSnapshotVersion, out.Version)
	}
	if err := validateSnapshot(out); err != nil {
		return snapshot{}, err
	}
	return out, nil
}

func validateSnapshot(data snapshot) error {
	sourceIDs := make(map[string]struct{}, len(data.Sources))
	for _, source := range data.Sources {
		if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.URI) == "" {
			return errors.New("source id and URI are required")
		}
		parsed, err := url.Parse(source.URI)
		if err != nil || parsed.Scheme == "" {
			return fmt.Errorf("source %q has invalid URI", source.ID)
		}
		if source.Confidence < 0 || source.Confidence > 1 {
			return fmt.Errorf("source %q has invalid confidence", source.ID)
		}
		if _, exists := sourceIDs[source.ID]; exists {
			return fmt.Errorf("duplicate source %q", source.ID)
		}
		sourceIDs[source.ID] = struct{}{}
	}
	evidenceIDs := make(map[string]struct{}, len(data.Evidence))
	for _, evidence := range data.Evidence {
		if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.Quote) == "" {
			return errors.New("evidence id and quote are required")
		}
		if _, exists := sourceIDs[evidence.SourceID]; !exists {
			return fmt.Errorf("evidence %q references missing source %q", evidence.ID, evidence.SourceID)
		}
		if evidence.Confidence < 0 || evidence.Confidence > 1 {
			return fmt.Errorf("evidence %q has invalid confidence", evidence.ID)
		}
		if _, exists := evidenceIDs[evidence.ID]; exists {
			return fmt.Errorf("duplicate evidence %q", evidence.ID)
		}
		evidenceIDs[evidence.ID] = struct{}{}
	}
	claimIDs := make(map[string]struct{}, len(data.Claims))
	for _, claim := range data.Claims {
		if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.Text) == "" {
			return errors.New("claim id and text are required")
		}
		if claim.Confidence < 0 || claim.Confidence > 1 {
			return fmt.Errorf("claim %q has invalid confidence", claim.ID)
		}
		if _, exists := claimIDs[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		claimIDs[claim.ID] = struct{}{}
	}
	for _, claim := range data.Claims {
		for _, evidenceID := range claim.EvidenceIDs {
			if _, exists := evidenceIDs[evidenceID]; !exists {
				return fmt.Errorf("claim %q references missing evidence %q", claim.ID, evidenceID)
			}
		}
		for _, contradictedID := range claim.Contradicts {
			if _, exists := claimIDs[contradictedID]; !exists {
				return fmt.Errorf("claim %q references missing contradiction %q", claim.ID, contradictedID)
			}
		}
	}
	return nil
}

func (s *Store) saveLocked(directory *os.Root, data snapshot) error {
	data.Version = 1
	if err := validateSnapshot(data); err != nil {
		return fmt.Errorf("invalid research mutation: %w", err)
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	existing, readErr := readStoreFile(directory, storeFileName)
	var backup []byte
	switch {
	case readErr == nil:
		if _, decodeErr := decodeSnapshot(existing); decodeErr == nil {
			backup = existing
		} else if errors.Is(decodeErr, errUnsupportedSnapshotVersion) {
			return decodeErr
		} else if err := s.validateBackup(directory); err != nil {
			return fmt.Errorf("refusing to replace corrupt research store: %w", err)
		}
	case os.IsNotExist(readErr):
		if err := s.validateBackup(directory); os.IsNotExist(err) {
			// Seed recovery on the first write, before publishing the primary.
			backup = encoded
		} else if err != nil {
			return err
		}
	default:
		return readErr
	}
	// Keep a validated recovery copy when the primary was corrupt or missing.
	if backup != nil {
		if err := writeAtomic(directory, storeFileName+".bak", backup); err != nil {
			return fmt.Errorf("write research backup: %w", err)
		}
	}
	return writeAtomic(directory, storeFileName, encoded)
}

func (s *Store) validateBackup(directory *os.Root) error {
	data, err := readStoreFile(directory, storeFileName+".bak")
	if err != nil {
		return err
	}
	_, err = decodeSnapshot(data)
	return err
}

func writeAtomic(directory *os.Root, name string, data []byte) error {
	if err := validateStoreFile(directory, name); err != nil {
		return err
	}
	temporary := ".research-" + uuid.NewString() + ".tmp"
	file, err := directory.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer directory.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := directory.Rename(temporary, name); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func hasSource(sources []Source, id string) bool {
	for _, source := range sources {
		if source.ID == id {
			return true
		}
	}
	return false
}

func normalize(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }
func hash(value string) string      { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
