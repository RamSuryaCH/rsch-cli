// Package optimizer shrinks the conversation history RSCH sends to a model on
// every request. It works on a copy of the history, so stored messages and
// the TUI are unchanged, and it never drops a message: tool results keep
// their call IDs so every provider still sees a well-formed conversation.
//
// Four passes run in order:
//
//  1. Deduplication: when the same tool output appears more than once in a
//     prompt (for example the same file read twice), the most recent copy is
//     kept and older copies become a short reference to it.
//  2. Trimming: long tool outputs keep their head and tail with a marker
//     saying how much was cut. Older outputs get a tighter cap than the most
//     recent ones, which the model is usually still working with.
//  3. Compaction: when the history is still above a share of the model's
//     context window, the oldest tool outputs are replaced by a stub until it
//     fits. This works in `rsch exec` as well as in the TUI.
//  4. Accounting: every request records estimated message tokens before and
//     after, per session.
//
// A per-session content-hash cache remembers how each distinct block was
// rewritten, so a block gets byte-identical replacement text on every later
// turn. That keeps the prompt prefix stable, which provider prompt caches
// depend on, and avoids trimming the same output twice.
//
// Set RSCH_OPTIMIZER=off (or pass --no-optimize) to send history unchanged.
package optimizer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/message"
)

// DisableEnv turns the optimizer off when set to off, 0, false or no.
const DisableEnv = "RSCH_OPTIMIZER"

// Options control the optimizer. Zero values fall back to DefaultOptions.
type Options struct {
	// MaxToolOutputBytes caps each of the most recent tool outputs.
	MaxToolOutputBytes int
	// StaleToolOutputBytes caps tool outputs older than the recent window.
	StaleToolOutputBytes int
	// RecentToolMessages is how many trailing tool-result messages count as
	// recent.
	RecentToolMessages int
	// MinDedupBytes is the smallest output worth replacing by a reference.
	MinDedupBytes int
	// CompactAtFraction is the share of the model's context window that the
	// message history may use before old tool outputs are compacted.
	CompactAtFraction float64
}

// DefaultOptions are used when the optimizer is on.
var DefaultOptions = Options{
	MaxToolOutputBytes:   16000,
	StaleToolOutputBytes: 4000,
	RecentToolMessages:   2,
	MinDedupBytes:        256,
	CompactAtFraction:    0.6,
}

// Stats describe one request, or a session's running totals.
type Stats struct {
	Requests     int  `json:"requests"`
	TokensBefore int  `json:"tokensBefore"`
	TokensAfter  int  `json:"tokensAfter"`
	Deduplicated int  `json:"deduplicated"`
	Trimmed      int  `json:"trimmed"`
	Compacted    int  `json:"compacted"`
	CacheHits    int  `json:"cacheHits"`
	Enabled      bool `json:"enabled"`
}

// Saved is the estimated number of tokens removed.
func (s Stats) Saved() int { return s.TokensBefore - s.TokensAfter }

func (s *Stats) add(other Stats) {
	s.Requests += other.Requests
	s.TokensBefore += other.TokensBefore
	s.TokensAfter += other.TokensAfter
	s.Deduplicated += other.Deduplicated
	s.Trimmed += other.Trimmed
	s.Compacted += other.Compacted
	s.CacheHits += other.CacheHits
	s.Enabled = s.Enabled || other.Enabled
}

// EstimateTokens matches the approximation used by `rsch context`: about
// four bytes per token. Use a real tokenizer (see bench/tokens) for numbers
// you publish.
func EstimateTokens(text string) int { return (len(text) + 3) / 4 }

// Enabled reports whether the optimizer is on (the default).
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DisableEnv))) {
	case "off", "0", "false", "no", "disabled":
		return false
	}
	return true
}

type cacheKey struct {
	hash  [32]byte
	limit int
}

type session struct {
	trimmed map[cacheKey]string
	totals  Stats
}

// Optimizer is safe for concurrent use by several sessions.
type Optimizer struct {
	options  Options
	mu       sync.Mutex
	sessions map[string]*session
}

// New returns an optimizer with options merged over DefaultOptions.
func New(options Options) *Optimizer {
	merged := DefaultOptions
	if options.MaxToolOutputBytes > 0 {
		merged.MaxToolOutputBytes = options.MaxToolOutputBytes
	}
	if options.StaleToolOutputBytes > 0 {
		merged.StaleToolOutputBytes = options.StaleToolOutputBytes
	}
	if options.RecentToolMessages > 0 {
		merged.RecentToolMessages = options.RecentToolMessages
	}
	if options.MinDedupBytes > 0 {
		merged.MinDedupBytes = options.MinDedupBytes
	}
	if options.CompactAtFraction > 0 {
		merged.CompactAtFraction = options.CompactAtFraction
	}
	return &Optimizer{options: merged, sessions: map[string]*session{}}
}

// Default is the optimizer the agent uses.
var Default = New(Options{})

// Totals returns the running totals for a session.
func (o *Optimizer) Totals(sessionID string) Stats {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.sessions[sessionID]; ok {
		return s.totals
	}
	return Stats{}
}

// Forget drops a session's cache and totals.
func (o *Optimizer) Forget(sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sessions, sessionID)
}

// HistoryTokens estimates the tokens in a message history.
func HistoryTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.TextContent:
				total += EstimateTokens(p.Text)
			case message.ToolCall:
				total += EstimateTokens(p.Name) + EstimateTokens(p.Input)
			case message.ToolResult:
				total += EstimateTokens(p.Content)
			}
		}
	}
	return total
}

type resultRef struct {
	message, part int
	recent        bool
}

// Optimize returns the history to send for this request and its stats. The
// input slice and its messages are not modified. contextWindow may be 0 when
// unknown, which disables compaction.
func (o *Optimizer) Optimize(sessionID string, history []message.Message, contextWindow int64) ([]message.Message, Stats) {
	stats := Stats{Requests: 1, TokensBefore: HistoryTokens(history)}
	if !Enabled() {
		stats.TokensAfter = stats.TokensBefore
		o.record(sessionID, stats)
		return history, stats
	}
	stats.Enabled = true

	out := make([]message.Message, len(history))
	for i, msg := range history {
		out[i] = msg
		out[i].Parts = append([]message.ContentPart(nil), msg.Parts...)
	}

	var refs []resultRef
	toolMessages := 0
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role != message.Tool {
			continue
		}
		recent := toolMessages < o.options.RecentToolMessages
		toolMessages++
		for j := len(out[i].Parts) - 1; j >= 0; j-- {
			if _, ok := out[i].Parts[j].(message.ToolResult); ok {
				refs = append(refs, resultRef{message: i, part: j, recent: recent})
			}
		}
	}
	o.mu.Lock()
	sess := o.sessions[sessionID]
	if sess == nil {
		sess = &session{trimmed: map[cacheKey]string{}}
		o.sessions[sessionID] = sess
	}
	o.mu.Unlock()

	// Pass 1: deduplicate. refs run newest first, so the most recent copy of
	// a block stays whole (the model asked for it again for a reason) and
	// older copies become references to it.
	latest := map[[32]byte]message.ToolResult{}
	for _, ref := range refs {
		result := out[ref.message].Parts[ref.part].(message.ToolResult)
		if len(result.Content) < o.options.MinDedupBytes {
			continue
		}
		hash := sha256.Sum256([]byte(result.Content))
		if later, seen := latest[hash]; seen {
			result.Content = fmt.Sprintf("[rsch optimizer: identical to the later output of %s (call %s); %d bytes omitted]", later.Name, later.ToolCallID, len(result.Content))
			out[ref.message].Parts[ref.part] = result
			stats.Deduplicated++
			continue
		}
		latest[hash] = result
	}

	// Pass 2: trim long outputs, reusing cached rewrites.
	for _, ref := range refs {
		result := out[ref.message].Parts[ref.part].(message.ToolResult)
		limit := o.options.StaleToolOutputBytes
		if ref.recent {
			limit = o.options.MaxToolOutputBytes
		}
		if len(result.Content) <= limit {
			continue
		}
		key := cacheKey{hash: sha256.Sum256([]byte(result.Content)), limit: limit}
		o.mu.Lock()
		trimmed, cached := sess.trimmed[key]
		o.mu.Unlock()
		if cached {
			stats.CacheHits++
		} else {
			trimmed = TrimMiddle(result.Content, limit)
			o.mu.Lock()
			sess.trimmed[key] = trimmed
			o.mu.Unlock()
		}
		result.Content = trimmed
		out[ref.message].Parts[ref.part] = result
		stats.Trimmed++
	}

	// Oldest first from here on.
	for left, right := 0, len(refs)-1; left < right; left, right = left+1, right-1 {
		refs[left], refs[right] = refs[right], refs[left]
	}

	// Pass 3: compact the oldest outputs until the history fits the budget.
	if contextWindow > 0 {
		budget := int(float64(contextWindow) * o.options.CompactAtFraction)
		current := HistoryTokens(out)
		for _, ref := range refs {
			if current <= budget {
				break
			}
			if ref.recent {
				continue
			}
			result := out[ref.message].Parts[ref.part].(message.ToolResult)
			stub := fmt.Sprintf("[rsch optimizer: output of %s omitted to fit the context window (%d bytes); call the tool again if you need it]", result.Name, len(result.Content))
			if len(stub) >= len(result.Content) {
				continue
			}
			current -= EstimateTokens(result.Content) - EstimateTokens(stub)
			result.Content = stub
			out[ref.message].Parts[ref.part] = result
			stats.Compacted++
		}
	}

	stats.TokensAfter = HistoryTokens(out)
	o.record(sessionID, stats)
	return out, stats
}

func (o *Optimizer) record(sessionID string, stats Stats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	sess := o.sessions[sessionID]
	if sess == nil {
		sess = &session{trimmed: map[cacheKey]string{}}
		o.sessions[sessionID] = sess
	}
	sess.totals.add(stats)
}

// TrimMiddle keeps about 60% of limit from the head of text and 40% from the
// tail, cut at line boundaries where possible, and puts a marker between
// them saying how many lines and bytes were removed.
func TrimMiddle(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	headBytes := limit * 6 / 10
	tailBytes := limit - headBytes
	head := text[:headBytes]
	if cut := strings.LastIndexByte(head, '\n'); cut > headBytes/2 {
		head = head[:cut+1]
	}
	tail := text[len(text)-tailBytes:]
	if cut := strings.IndexByte(tail, '\n'); cut >= 0 && cut < tailBytes/2 {
		tail = tail[cut+1:]
	}
	middle := text[len(head) : len(text)-len(tail)]
	lines := strings.Count(middle, "\n")
	marker := fmt.Sprintf("\n[rsch optimizer: trimmed %d lines (%d bytes) from the middle of this output; rerun with a narrower command or read a smaller range to see them]\n", lines, len(middle))
	return strings.TrimRight(head, "\n") + marker + tail
}
