// Package memory provides inspectable, scoped, removable project memory.
package memory

import (
	"bytes"
	"crypto/sha256"
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
)

type Layer string

const (
	LayerGlobal    Layer = "global"
	LayerUser      Layer = "user"
	LayerWorkspace Layer = "workspace"
	LayerProject   Layer = "project"
	LayerSession   Layer = "session"
	LayerTask      Layer = "task"
)

type Kind string

const (
	KindSemantic   Kind = "semantic"
	KindEpisodic   Kind = "episodic"
	KindProcedural Kind = "procedural"
	KindResearch   Kind = "research"
)

type Provenance struct {
	SourceType string    `json:"sourceType,omitempty"`
	SourceID   string    `json:"sourceId,omitempty"`
	Location   string    `json:"location,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	Method     string    `json:"method,omitempty"`
	ObservedAt time.Time `json:"observedAt,omitempty"`
}

type Relation struct {
	Type    string `json:"type"`
	EntryID string `json:"entryId"`
}

type Entry struct {
	ID          string     `json:"id"`
	Category    string     `json:"category"`
	Scope       string     `json:"scope"`
	ScopeID     string     `json:"scopeId,omitempty"`
	Layer       Layer      `json:"layer,omitempty"`
	Kind        Kind       `json:"kind,omitempty"`
	Content     string     `json:"content"`
	Tags        []string   `json:"tags,omitempty"`
	Source      string     `json:"source,omitempty"`
	Provenance  Provenance `json:"provenance,omitempty"`
	Importance  float64    `json:"importance,omitempty"`
	Confidence  float64    `json:"confidence,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	Status      string     `json:"status,omitempty"`
	Relations   []Relation `json:"relations,omitempty"`
	ContentHash string     `json:"contentHash,omitempty"`
	AccessedAt  time.Time  `json:"accessedAt,omitempty"`
	AccessCount int64      `json:"accessCount,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type SearchResult struct {
	Entry   Entry    `json:"entry"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}

const (
	storeSchemaVersion = 1
	maxStoreBytes      = 16 << 20
	lockRetryInterval  = 10 * time.Millisecond
	lockWaitTimeout    = 5 * time.Second
	staleLockAge       = 2 * time.Minute
)

type persistedStore struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	Checksum  string    `json:"checksum"`
	Entries   []Entry   `json:"entries"`
}

type Store struct {
	path string
	mu   *sync.Mutex
}

var storeLocks sync.Map

var errUnsupportedStoreVersion = errors.New("unsupported memory store version")

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("memory root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect memory root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("memory root is not a directory")
	}
	dataDir := filepath.Join(absolute, ".rsch")
	if err := ensureDirectory(dataDir, false); err != nil {
		return nil, err
	}
	memoryDir := filepath.Join(dataDir, "memory")
	if err := ensureDirectory(memoryDir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(memoryDir, "entries.json")
	for _, candidate := range []string{path, path + ".bak"} {
		if err := secureExistingFile(candidate); err != nil {
			return nil, err
		}
	}
	lock, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	return &Store{path: path, mu: lock.(*sync.Mutex)}, nil
}

func (s *Store) Add(entry Entry) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("memory store is required")
	}
	if strings.TrimSpace(entry.Content) == "" {
		return Entry{}, errors.New("memory content is required")
	}
	if entry.Category == "" {
		entry.Category = "fact"
	}
	if entry.Scope == "" {
		entry.Scope = "repository"
	}
	entry.ScopeID = strings.TrimSpace(entry.ScopeID)
	if entry.Layer == "" {
		entry.Layer = layerForScope(entry.Scope)
	}
	if entry.Kind == "" {
		entry.Kind = KindSemantic
	}
	if entry.Importance == 0 {
		entry.Importance = 0.5
	}
	if entry.Confidence == 0 {
		entry.Confidence = 0.5
	}
	if entry.Importance < 0 || entry.Importance > 1 || entry.Confidence < 0 || entry.Confidence > 1 {
		return Entry{}, errors.New("memory importance and confidence must be between 0 and 1")
	}
	if entry.Status == "" {
		entry.Status = "active"
	}
	entry.ContentHash = contentHash(entry.Content)
	if entry.Provenance.ObservedAt.IsZero() {
		entry.Provenance.ObservedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	entry.CreatedAt = now
	entry.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return Entry{}, err
	}
	defer release()
	entries, err := s.loadLocked()
	if err != nil {
		return Entry{}, err
	}
	entries, removed := removeExpired(entries, now)
	for _, existing := range entries {
		if existing.Status != "deleted" && existing.ContentHash == entry.ContentHash && existing.Scope == entry.Scope && existing.ScopeID == entry.ScopeID && existing.Layer == entry.Layer && !expired(existing) {
			if removed > 0 {
				if err := s.saveLocked(entries); err != nil {
					return Entry{}, err
				}
			}
			return existing, nil
		}
		if existing.ID == entry.ID {
			return Entry{}, fmt.Errorf("memory entry %q already exists", entry.ID)
		}
	}
	entries = append(entries, entry)
	if err := s.saveLocked(entries); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Store) Remove(id string) error {
	if s == nil {
		return errors.New("memory store is required")
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("memory id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer release()
	entries, err := s.loadLocked()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	found := false
	for _, entry := range entries {
		if entry.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, entry)
	}
	if !found {
		return fmt.Errorf("memory entry %q not found", id)
	}
	return s.saveLocked(filtered)
}

func (s *Store) List(scope string) ([]Entry, error) {
	if s == nil {
		return nil, errors.New("memory store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	if scope != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if (entry.Scope == scope || string(entry.Layer) == scope) && !expired(entry) && entry.Status != "deleted" {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	entries = activeEntries(entries)
	sortEntries(entries)
	return append([]Entry(nil), entries...), nil
}

func (s *Store) Search(query, scope string) ([]Entry, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("memory search query is required")
	}
	results, err := s.SearchRanked(query, scope, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(results))
	for _, result := range results {
		out = append(out, result.Entry)
	}
	return out, nil
}

func (s *Store) SearchRanked(query, scope string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("memory search query is required")
	}
	entries, err := s.List(scope)
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(query))
	results := make([]SearchResult, 0, len(entries))
	for _, entry := range entries {
		haystack := strings.ToLower(entry.Category + " " + string(entry.Kind) + " " + string(entry.Layer) + " " + entry.Scope + " " + entry.Content + " " + strings.Join(entry.Tags, " "))
		score := 0.0
		matched := 0
		for _, term := range terms {
			if strings.Contains(strings.ToLower(entry.Content), term) {
				score += 3
				matched++
			} else if strings.Contains(haystack, term) {
				score++
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		score += entry.Importance + entry.Confidence
		if !entry.UpdatedAt.IsZero() {
			days := time.Since(entry.UpdatedAt).Hours() / 24
			if days < 0 {
				days = 0
			}
			score += 1 / (1 + days/30)
		}
		reasons := []string{"term match"}
		if entry.Importance >= 0.75 {
			reasons = append(reasons, "high importance")
		}
		if entry.Confidence >= 0.75 {
			reasons = append(reasons, "high confidence")
		}
		results = append(results, SearchResult{Entry: entry, Score: score, Reasons: reasons})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Entry.ID < results[j].Entry.ID
		}
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Store) FindRelations(id string) ([]Relation, error) {
	entry, err := s.Explain(id)
	if err != nil {
		return nil, err
	}
	return append([]Relation(nil), entry.Relations...), nil
}

func (s *Store) ForgetExpired() (int, error) {
	if s == nil {
		return 0, errors.New("memory store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.acquireFileLock()
	if err != nil {
		return 0, err
	}
	defer release()
	entries, err := s.loadLocked()
	if err != nil {
		return 0, err
	}
	kept, removed := removeExpired(entries, time.Now().UTC())
	if removed > 0 {
		if err := s.saveLocked(kept); err != nil {
			return 0, err
		}
	}
	return removed, nil
}

func (s *Store) Explain(id string) (Entry, error) {
	entries, err := s.List("")
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return Entry{}, fmt.Errorf("memory entry %q not found", id)
}

func (s *Store) loadLocked() ([]Entry, error) {
	data, err := readStoreFile(s.path)
	if os.IsNotExist(err) {
		return s.recoverFromBackup(nil, err)
	}
	if err != nil {
		return nil, err
	}
	entries, _, decodeErr := decodeStore(data)
	if decodeErr == nil {
		return entries, nil
	}
	if errors.Is(decodeErr, errUnsupportedStoreVersion) {
		return nil, decodeErr
	}
	return s.recoverFromBackup(data, decodeErr)
}

func (s *Store) saveLocked(entries []Entry) error {
	data, err := encodeStore(entries)
	if err != nil {
		return err
	}
	existing, readErr := readStoreFile(s.path)
	switch {
	case readErr == nil:
		if _, _, err := decodeStore(existing); err != nil {
			return fmt.Errorf("refusing to replace invalid memory store without recovery: %w", err)
		}
		if err := writeAtomic(s.path+".bak", existing); err != nil {
			return fmt.Errorf("write memory backup: %w", err)
		}
	case os.IsNotExist(readErr):
		// The first successful write must also be recoverable. Seed the backup
		// before publishing the primary file so either copy is durable.
		if err := writeAtomic(s.path+".bak", data); err != nil {
			return fmt.Errorf("write initial memory backup: %w", err)
		}
	default:
		return readErr
	}
	if err := writeAtomic(s.path, data); err != nil {
		return fmt.Errorf("write memory store: %w", err)
	}
	return nil
}

func encodeStore(entries []Entry) ([]byte, error) {
	entriesJSON, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	persisted := persistedStore{
		Version:   storeSchemaVersion,
		UpdatedAt: time.Now().UTC(),
		Checksum:  fmt.Sprintf("%x", sha256.Sum256(entriesJSON)),
		Entries:   entries,
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maxStoreBytes {
		return nil, fmt.Errorf("memory store exceeds %d bytes", maxStoreBytes)
	}
	return append(data, '\n'), nil
}

func decodeStore(data []byte) ([]Entry, bool, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, false, errors.New("memory store is empty")
	}
	if data[0] == '[' {
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, true, err
		}
		return entries, true, nil
	}
	var persisted persistedStore
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, false, err
	}
	if persisted.Version != storeSchemaVersion {
		return nil, false, fmt.Errorf("%w %d", errUnsupportedStoreVersion, persisted.Version)
	}
	if persisted.Entries == nil {
		persisted.Entries = []Entry{}
	}
	entriesJSON, err := json.Marshal(persisted.Entries)
	if err != nil {
		return nil, false, err
	}
	want := fmt.Sprintf("%x", sha256.Sum256(entriesJSON))
	if persisted.Checksum == "" || persisted.Checksum != want {
		return nil, false, errors.New("memory store checksum mismatch")
	}
	return persisted.Entries, false, nil
}

func readStoreFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("memory store path is not a regular file: %s", path)
	}
	if info.Size() > maxStoreBytes {
		return nil, fmt.Errorf("memory store exceeds %d bytes", maxStoreBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStoreBytes {
		return nil, fmt.Errorf("memory store exceeds %d bytes", maxStoreBytes)
	}
	return data, nil
}

func (s *Store) recoverFromBackup(corrupt []byte, primaryErr error) ([]Entry, error) {
	backupPath := s.path + ".bak"
	backup, backupErr := readStoreFile(backupPath)
	if backupErr != nil {
		if os.IsNotExist(primaryErr) && os.IsNotExist(backupErr) {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("invalid memory store (%v); backup unavailable: %w", primaryErr, backupErr)
	}
	entries, _, decodeErr := decodeStore(backup)
	if decodeErr != nil {
		return nil, fmt.Errorf("invalid memory store (%v); invalid backup: %w", primaryErr, decodeErr)
	}
	if corrupt != nil {
		quarantine := fmt.Sprintf("%s.corrupt.%d", s.path, time.Now().UTC().UnixNano())
		if err := os.Rename(s.path, quarantine); err != nil {
			return nil, fmt.Errorf("quarantine corrupt memory store: %w", err)
		}
	}
	if err := writeAtomic(s.path, backup); err != nil {
		return nil, fmt.Errorf("restore memory backup: %w", err)
	}
	return entries, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func (s *Store) acquireFileLock() (func(), error) {
	lockPath := s.path + ".lock"
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintf(file, "%d\n%d\n", os.Getpid(), time.Now().UTC().UnixNano())
			if writeErr == nil {
				writeErr = file.Sync()
			}
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(lockPath)
				if writeErr != nil {
					return nil, writeErr
				}
				return nil, closeErr
			}
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("lock memory store: %w", err)
		}
		info, statErr := os.Lstat(lockPath)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("memory lock path cannot be a symlink")
			}
			if time.Since(info.ModTime()) > staleLockAge {
				if removeErr := os.Remove(lockPath); removeErr == nil || os.IsNotExist(removeErr) {
					continue
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for memory store lock")
		}
		time.Sleep(lockRetryInterval)
	}
}

func ensureDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create memory directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("memory data path is not a directory: %s", path)
	}
	if private {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func secureExistingFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("memory store path is not a regular file: %s", path)
	}
	return os.Chmod(path, 0o600)
}

func removeExpired(entries []Entry, now time.Time) ([]Entry, int) {
	kept := entries[:0]
	removed := 0
	for _, entry := range entries {
		if expiredAt(entry, now) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	return kept, removed
}
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].UpdatedAt.Before(entries[j].UpdatedAt)
	})
}

func activeEntries(entries []Entry) []Entry {
	out := entries[:0]
	for _, entry := range entries {
		if entry.Status != "deleted" && !expired(entry) {
			out = append(out, entry)
		}
	}
	return out
}

func expired(entry Entry) bool {
	return expiredAt(entry, time.Now().UTC())

}

func expiredAt(entry Entry, now time.Time) bool {
	return entry.ExpiresAt != nil && !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now)
}

func layerForScope(scope string) Layer {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global":
		return LayerGlobal
	case "user":
		return LayerUser
	case "workspace":
		return LayerWorkspace
	case "project", "repository":
		return LayerProject
	case "session":
		return LayerSession
	case "task":
		return LayerTask
	default:
		return LayerProject
	}
}

func contentHash(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(content))))
}
