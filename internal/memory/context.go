package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultContextTokenBudget = 2048
	MaxContextTokenBudget     = 32768
	DefaultContextEntries     = 8
	MaxContextEntries         = 64
)

// ConflictPolicy controls how explicitly contradictory memories are handled.
// Superseded memories are always omitted.
type ConflictPolicy string

const (
	ConflictPreferFresh ConflictPolicy = "prefer-fresh"
	ConflictExclude     ConflictPolicy = "exclude"
	ConflictInclude     ConflictPolicy = "include"
)

// ContextScope selects one memory layer and, for owned layers such as session
// and task, the concrete owner. An empty ID only matches legacy unowned entries.
type ContextScope struct {
	Layer Layer  `json:"layer"`
	ID    string `json:"id,omitempty"`
}

// TokenCounter lets an integration provide the active model's tokenizer. The
// default counter charges one token per byte, a conservative upper bound for
// byte-based tokenizers.
type TokenCounter func(string) int

// ContextOptions configures deterministic, bounded memory retrieval.
type ContextOptions struct {
	Query             string         `json:"query"`
	Scopes            []ContextScope `json:"scopes,omitempty"`
	TokenBudget       int            `json:"tokenBudget,omitempty"`
	MaxEntries        int            `json:"maxEntries,omitempty"`
	MinimumConfidence float64        `json:"minimumConfidence,omitempty"`
	MaxAge            time.Duration  `json:"maxAge,omitempty"`
	ConflictPolicy    ConflictPolicy `json:"conflictPolicy,omitempty"`
	Now               time.Time      `json:"now,omitempty"`
	TokenCounter      TokenCounter   `json:"-"`
}

type ContextItem struct {
	Entry         Entry      `json:"entry"`
	Score         float64    `json:"score"`
	Reasons       []string   `json:"reasons,omitempty"`
	ConflictsWith []string   `json:"conflictsWith,omitempty"`
	Tokens        int        `json:"tokens"`
	ObservedAt    *time.Time `json:"observedAt,omitempty"`
}

type ContextExclusion struct {
	EntryID string `json:"entryId"`
	Reason  string `json:"reason"`
}

// ContextResult contains both structured records and newline-delimited JSON
// suitable for placing in an agent context without losing provenance.
type ContextResult struct {
	Query       string             `json:"query"`
	Items       []ContextItem      `json:"items"`
	Text        string             `json:"text"`
	Budget      int                `json:"budget"`
	Used        int                `json:"used"`
	Excluded    []ContextExclusion `json:"excluded,omitempty"`
	GeneratedAt time.Time          `json:"generatedAt"`
}

type contextCandidate struct {
	item ContextItem
}

// RetrieveContext returns scope-safe, freshness-filtered, conflict-resolved,
// relevance-ranked memory under a hard caller-visible token budget.
func (s *Store) RetrieveContext(options ContextOptions) (ContextResult, error) {
	if s == nil {
		return ContextResult{}, errors.New("memory store is required")
	}
	options.Query = strings.TrimSpace(options.Query)
	if options.Query == "" {
		return ContextResult{}, errors.New("memory context query is required")
	}
	if err := normalizeContextOptions(&options); err != nil {
		return ContextResult{}, err
	}

	s.mu.Lock()
	release, err := s.acquireFileLock()
	if err != nil {
		s.mu.Unlock()
		return ContextResult{}, err
	}
	entries, err := s.loadLocked()
	release()
	s.mu.Unlock()
	if err != nil {
		return ContextResult{}, err
	}

	result := ContextResult{Query: options.Query, Items: []ContextItem{}, Budget: options.TokenBudget, GeneratedAt: options.Now}
	candidates := make([]contextCandidate, 0, len(entries))
	eligible := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		if !matchesContextScopes(entry, options.Scopes) {
			continue
		}
		if entry.Status == "deleted" || entry.Status == "superseded" || expiredAt(entry, options.Now) {
			result.Excluded = append(result.Excluded, ContextExclusion{EntryID: entry.ID, Reason: "inactive or expired"})
			continue
		}
		if entry.Confidence < options.MinimumConfidence {
			result.Excluded = append(result.Excluded, ContextExclusion{EntryID: entry.ID, Reason: "below minimum confidence"})
			continue
		}
		observed := entryObservedAt(entry)
		if options.MaxAge > 0 && !observed.IsZero() && options.Now.Sub(observed) > options.MaxAge {
			result.Excluded = append(result.Excluded, ContextExclusion{EntryID: entry.ID, Reason: "stale"})
			continue
		}
		if entry.Layer == "" {
			entry.Layer = layerForScope(entry.Scope)
		}
		eligible[entry.ID] = entry
		score, reasons, matched := scoreContextEntry(entry, options.Query, options.Now)
		if !matched {
			continue
		}
		item := ContextItem{Entry: entry, Score: score, Reasons: reasons}
		if !observed.IsZero() {
			value := observed
			item.ObservedAt = &value
		}
		candidates = append(candidates, contextCandidate{item: item})
	}

	candidates, conflictExclusions := resolveContextConflicts(candidates, eligible, options.ConflictPolicy)
	result.Excluded = append(result.Excluded, conflictExclusions...)
	sortContextCandidates(candidates)

	selectedLines := make([]string, 0, min(options.MaxEntries, len(candidates)))
	for _, candidate := range candidates {
		if len(result.Items) >= options.MaxEntries {
			result.Excluded = append(result.Excluded, ContextExclusion{EntryID: candidate.item.Entry.ID, Reason: "entry limit"})
			continue
		}
		line, err := renderContextItem(candidate.item)
		if err != nil {
			return ContextResult{}, err
		}
		candidate.item.Tokens, err = countContextTokens(options.TokenCounter, line)
		if err != nil {
			return ContextResult{}, err
		}
		proposedLines := append(append([]string(nil), selectedLines...), line)
		proposedText := strings.Join(proposedLines, "\n")
		proposedTokens, err := countContextTokens(options.TokenCounter, proposedText)
		if err != nil {
			return ContextResult{}, err
		}
		if proposedTokens > options.TokenBudget {
			result.Excluded = append(result.Excluded, ContextExclusion{EntryID: candidate.item.Entry.ID, Reason: "token budget"})
			continue
		}
		selectedLines = proposedLines
		result.Items = append(result.Items, candidate.item)
		result.Text = proposedText
		result.Used = proposedTokens
	}
	sort.SliceStable(result.Excluded, func(i, j int) bool {
		if result.Excluded[i].EntryID == result.Excluded[j].EntryID {
			return result.Excluded[i].Reason < result.Excluded[j].Reason
		}
		return result.Excluded[i].EntryID < result.Excluded[j].EntryID
	})
	return result, nil
}

func normalizeContextOptions(options *ContextOptions) error {
	if options.TokenBudget < 0 || options.TokenBudget > MaxContextTokenBudget {
		return fmt.Errorf("memory context token budget must be between 0 and %d", MaxContextTokenBudget)
	}
	if options.TokenBudget == 0 {
		options.TokenBudget = DefaultContextTokenBudget
	}
	if options.MaxEntries < 0 || options.MaxEntries > MaxContextEntries {
		return fmt.Errorf("memory context entry limit must be between 0 and %d", MaxContextEntries)
	}
	if options.MaxEntries == 0 {
		options.MaxEntries = DefaultContextEntries
	}
	if options.MinimumConfidence < 0 || options.MinimumConfidence > 1 {
		return errors.New("memory context minimum confidence must be between 0 and 1")
	}
	if options.MaxAge < 0 {
		return errors.New("memory context maximum age cannot be negative")
	}
	if options.ConflictPolicy == "" {
		options.ConflictPolicy = ConflictPreferFresh
	}
	switch options.ConflictPolicy {
	case ConflictPreferFresh, ConflictExclude, ConflictInclude:
	default:
		return fmt.Errorf("unsupported memory conflict policy %q", options.ConflictPolicy)
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	} else {
		options.Now = options.Now.UTC()
	}
	if options.TokenCounter == nil {
		options.TokenCounter = func(value string) int { return len([]byte(value)) }
	}
	for _, scope := range options.Scopes {
		if !validLayer(scope.Layer) {
			return fmt.Errorf("unsupported memory context layer %q", scope.Layer)
		}
	}
	return nil
}

func matchesContextScopes(entry Entry, scopes []ContextScope) bool {
	layer := entry.Layer
	if layer == "" {
		layer = layerForScope(entry.Scope)
	}
	if len(scopes) == 0 {
		return layer != LayerSession && layer != LayerTask && entry.ScopeID == ""
	}
	for _, scope := range scopes {
		if layer == scope.Layer && entry.ScopeID == scope.ID {
			return true
		}
	}
	return false
}

func scoreContextEntry(entry Entry, query string, now time.Time) (float64, []string, bool) {
	terms := strings.Fields(strings.ToLower(query))
	content := strings.ToLower(entry.Content)
	metadata := strings.ToLower(strings.Join([]string{entry.Category, string(entry.Kind), string(entry.Layer), entry.Scope, entry.ScopeID, entry.Source, entry.Provenance.SourceType, entry.Provenance.SourceID, entry.Provenance.Location, strings.Join(entry.Tags, " ")}, " "))
	score := 0.0
	matched := false
	reasons := make([]string, 0, 5)
	for _, term := range terms {
		if strings.Contains(content, term) {
			score += 3
			matched = true
		} else if strings.Contains(metadata, term) {
			score++
			matched = true
		}
	}
	if !matched {
		return 0, nil, false
	}
	if phrase := strings.ToLower(strings.TrimSpace(query)); strings.Contains(content, phrase) {
		score += 4
		reasons = append(reasons, "phrase match")
	} else {
		reasons = append(reasons, "term match")
	}
	score += entry.Importance + entry.Confidence
	if entry.Importance >= 0.75 {
		reasons = append(reasons, "high importance")
	}
	if entry.Confidence >= 0.75 {
		reasons = append(reasons, "high confidence")
	}
	observed := entryObservedAt(entry)
	if !observed.IsZero() {
		age := now.Sub(observed)
		if age < 0 {
			age = 0
		}
		score += 1 / (1 + age.Hours()/24/30)
		reasons = append(reasons, "freshness weighted")
	}
	score += float64(layerPriority(entry.Layer)) / 100
	return score, reasons, true
}

func resolveContextConflicts(candidates []contextCandidate, eligible map[string]Entry, policy ConflictPolicy) ([]contextCandidate, []ContextExclusion) {
	superseded := make(map[string]bool)
	conflicts := make(map[string]map[string]bool)
	for _, entry := range eligible {
		for _, relation := range entry.Relations {
			if _, exists := eligible[relation.EntryID]; !exists {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(relation.Type)) {
			case "supersedes":
				superseded[relation.EntryID] = true
			case "contradicts", "conflicts":
				addConflict(conflicts, entry.ID, relation.EntryID)
				addConflict(conflicts, relation.EntryID, entry.ID)
			}
		}
	}
	// A superseded record no longer participates in conflict resolution, just
	// like a record explicitly marked with Status "superseded" during retrieval.
	for id, peers := range conflicts {
		if superseded[id] {
			delete(conflicts, id)
			continue
		}
		for peer := range peers {
			if superseded[peer] {
				delete(peers, peer)
			}
		}
	}
	excluded := make([]ContextExclusion, 0)
	remaining := make([]contextCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if superseded[candidate.item.Entry.ID] {
			excluded = append(excluded, ContextExclusion{EntryID: candidate.item.Entry.ID, Reason: "superseded"})
			continue
		}
		if peers := conflicts[candidate.item.Entry.ID]; len(peers) > 0 {
			candidate.item.ConflictsWith = sortedKeys(peers)
		}
		remaining = append(remaining, candidate)
	}
	if policy == ConflictInclude {
		return remaining, excluded
	}
	if policy == ConflictExclude {
		kept := remaining[:0]
		for _, candidate := range remaining {
			if len(candidate.item.ConflictsWith) > 0 {
				excluded = append(excluded, ContextExclusion{EntryID: candidate.item.Entry.ID, Reason: "conflicting memory"})
				continue
			}
			kept = append(kept, candidate)
		}
		return kept, excluded
	}

	kept := make([]contextCandidate, 0, len(remaining))
	for _, candidate := range remaining {
		blocked := false
		for _, peer := range candidate.item.ConflictsWith {
			if fresherEntry(eligible[peer], candidate.item.Entry) {
				blocked = true
				break
			}
		}
		if blocked {
			excluded = append(excluded, ContextExclusion{EntryID: candidate.item.Entry.ID, Reason: "older conflicting memory"})
			continue
		}
		kept = append(kept, candidate)
	}
	return kept, excluded
}

func fresherEntry(left, right Entry) bool {
	leftTime, rightTime := entryObservedAt(left), entryObservedAt(right)
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	if left.Confidence != right.Confidence {
		return left.Confidence > right.Confidence
	}
	if layerPriority(left.Layer) != layerPriority(right.Layer) {
		return layerPriority(left.Layer) > layerPriority(right.Layer)
	}
	return left.ID < right.ID
}

func sortContextCandidates(candidates []contextCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].item.Score != candidates[j].item.Score {
			return candidates[i].item.Score > candidates[j].item.Score
		}
		return candidates[i].item.Entry.ID < candidates[j].item.Entry.ID
	})
}

func renderContextItem(item ContextItem) (string, error) {
	value := struct {
		ID         string     `json:"id"`
		Layer      Layer      `json:"layer"`
		Scope      string     `json:"scope"`
		ScopeID    string     `json:"scopeId,omitempty"`
		Kind       Kind       `json:"kind"`
		Content    string     `json:"content"`
		Source     string     `json:"source,omitempty"`
		Provenance Provenance `json:"provenance,omitempty"`
		Conflicts  []string   `json:"conflictsWith,omitempty"`
	}{ID: item.Entry.ID, Layer: item.Entry.Layer, Scope: item.Entry.Scope, ScopeID: item.Entry.ScopeID, Kind: item.Entry.Kind, Content: item.Entry.Content, Source: item.Entry.Source, Provenance: item.Entry.Provenance, Conflicts: item.ConflictsWith}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal memory context entry %q: %w", item.Entry.ID, err)
	}
	return string(data), nil
}

func entryObservedAt(entry Entry) time.Time {
	if !entry.Provenance.ObservedAt.IsZero() {
		return entry.Provenance.ObservedAt.UTC()
	}
	if !entry.UpdatedAt.IsZero() {
		return entry.UpdatedAt.UTC()
	}
	return entry.CreatedAt.UTC()
}

func countContextTokens(counter TokenCounter, value string) (int, error) {
	count := counter(value)
	if count < 0 {
		return 0, errors.New("memory context token counter returned a negative value")
	}
	return count, nil
}

func validLayer(layer Layer) bool {
	switch layer {
	case LayerGlobal, LayerUser, LayerWorkspace, LayerProject, LayerSession, LayerTask:
		return true
	default:
		return false
	}
}

func layerPriority(layer Layer) int {
	switch layer {
	case LayerTask:
		return 6
	case LayerSession:
		return 5
	case LayerProject:
		return 4
	case LayerWorkspace:
		return 3
	case LayerUser:
		return 2
	case LayerGlobal:
		return 1
	default:
		return 0
	}
}

func addConflict(conflicts map[string]map[string]bool, id, peer string) {
	if id == peer {
		return
	}
	if conflicts[id] == nil {
		conflicts[id] = make(map[string]bool)
	}
	conflicts[id][peer] = true
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
