// Package server exposes the client-independent workflow and event boundary.
// It is intentionally small and HTTP-native so future clients can reuse the
// same service without importing TUI internals.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/audit"
	"github.com/opencode-ai/opencode/internal/background"
	"github.com/opencode-ai/opencode/internal/confidence"
	"github.com/opencode-ai/opencode/internal/config"
	rschdb "github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/dbintel"
	"github.com/opencode-ai/opencode/internal/dependencies"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/evidence"
	"github.com/opencode-ai/opencode/internal/intelligence"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/performance"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/steering"
)

type GoalStore interface {
	PutGoal(engineering.Goal) error
	Goals() []engineering.Goal
	Tasks() []engineering.Task
	SaveRequirement(context.Context, engineering.Requirement) error
	Requirements(context.Context, string) ([]engineering.Requirement, error)
	ReplaceTasks(context.Context, string, []engineering.Task) error
	LoadTasks(context.Context, string) ([]engineering.Task, error)
	TransitionTask(context.Context, string, engineering.Status) (engineering.Task, error)
}
type MutationPolicy func(*http.Request, string) error

const approvalHeader = "X-RSCH-Approval"

type Server struct {
	bus            *events.Bus
	store          GoalStore
	log            *events.SQLLog
	mux            *http.ServeMux
	root           string
	db             *sql.DB
	authToken      string
	mutationPolicy MutationPolicy
	// Legacy GoalStore adapters may lack storage-level task transactions.
	// Serialize their mutations across both URL forms within this server.
	taskMutationMu sync.Mutex
}

func New(bus *events.Bus, store GoalStore) *Server {
	if bus == nil {
		bus = events.NewBus()
	}
	if store == nil {
		store = engineering.NewStore()
	}
	s := &Server{bus: bus, store: store, mux: http.NewServeMux()}
	s.routes()
	return s
}
func NewWithLog(bus *events.Bus, store GoalStore, log *events.SQLLog) *Server {
	service := New(bus, store)
	service.log = log
	return service
}

// NewWithWorkspace enables read-only inspection endpoints backed by the same
// workspace used by the CLI. The caller retains ownership of db.
func NewWithWorkspace(bus *events.Bus, store GoalStore, log *events.SQLLog, root string, db *sql.DB) *Server {
	s := NewWithLog(bus, store, log)
	s.root = root
	s.db = db
	return s
}

// SetAuthToken enables bearer-token authentication for every endpoint except
// the intentionally minimal health check. Tokens are held in memory only and
// are never included in logs or response bodies.
func (s *Server) SetAuthToken(token string) *Server {
	s.authToken = strings.TrimSpace(token)
	return s
}
func (s *Server) SetMutationPolicy(policy MutationPolicy) *Server {
	s.mutationPolicy = policy
	return s
}
func (s *Server) Handler() http.Handler { return s }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A loopback listener is still reachable by untrusted web pages. Validate
	// the authority and browser provenance before authentication or routing so
	// every endpoint, including the SSE stream, shares the same boundary.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Add("Vary", "Origin")
	requestHost, err := parseHTTPAuthority(r.Host, requestScheme(r))
	if err != nil || (s.authToken == "" && !isLoopbackHostname(requestHost.hostname)) {
		writeError(w, http.StatusMisdirectedRequest, fmt.Errorf("request host is not allowed"))
		return
	}
	if err := validateRequestOrigin(r, requestHost); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if s.authToken != "" && r.URL.Path != "/healthz" && !authorized(r, s.authToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="rsch"`)
		writeError(w, http.StatusUnauthorized, fmt.Errorf("authentication required"))
		return
	}
	if requestHasBody(r) && methodRequiresJSON(r.Method) && !hasJSONContentType(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, fmt.Errorf("Content-Type must be application/json"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

type httpAuthority struct {
	hostname string
	port     string
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func parseHTTPAuthority(authority, scheme string) (httpAuthority, error) {
	if authority == "" || strings.TrimSpace(authority) != authority || strings.ContainsAny(authority, "/?#@\x00\r\n\t") {
		return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
	}
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Host != authority || parsed.User != nil || parsed.Hostname() == "" {
		return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	port := parsed.Port()
	if strings.HasPrefix(authority, "[") {
		closingBracket := strings.IndexByte(authority, ']')
		if closingBracket < 0 || net.ParseIP(hostname) == nil {
			return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
		}
		suffix := authority[closingBracket+1:]
		if suffix != "" && (len(suffix) < 2 || suffix[0] != ':') {
			return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
		}
	} else if colonCount := strings.Count(authority, ":"); colonCount > 1 || colonCount == 1 && port == "" {
		// IP literals must be bracketed and an explicit port cannot be empty.
		return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
	}
	if port == "" {
		port = defaultPort(scheme)
	} else if value, parseErr := strconv.Atoi(port); parseErr != nil || value < 1 || value > 65535 {
		return httpAuthority{}, fmt.Errorf("invalid HTTP authority")
	}
	return httpAuthority{hostname: hostname, port: port}, nil
}

func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(strings.TrimSuffix(hostname, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func validateRequestOrigin(r *http.Request, requestHost httpAuthority) error {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		// Non-browser clients do not normally send Origin and remain supported.
		return nil
	}
	if len(origins) != 1 || strings.Contains(origins[0], ",") {
		return fmt.Errorf("request origin is not allowed")
	}
	raw := strings.TrimSpace(origins[0])
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Opaque != "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return fmt.Errorf("request origin is not allowed")
	}
	scheme := strings.ToLower(origin.Scheme)
	if scheme != "http" && scheme != "https" || scheme != requestScheme(r) {
		return fmt.Errorf("request origin is not allowed")
	}
	originHost, err := parseHTTPAuthority(origin.Host, scheme)
	if err != nil || !strings.EqualFold(originHost.hostname, requestHost.hostname) || originHost.port != requestHost.port {
		return fmt.Errorf("request origin is not allowed")
	}
	return nil
}

func requestHasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

func methodRequiresJSON(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func hasJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func authorized(r *http.Request, expected string) bool {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < len("Bearer ") || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(value[len("Bearer "):])
	if provided == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.health)
	s.mux.HandleFunc("/v1/status", s.status)
	s.mux.HandleFunc("/v1/goals", s.goals)
	s.mux.HandleFunc("/v1/goals/", s.goalResource)
	s.mux.HandleFunc("/v1/tasks", s.tasks)
	s.mux.HandleFunc("/v1/tasks/", s.taskResource)
	s.mux.HandleFunc("/v1/events", s.events)
	s.mux.HandleFunc("/v1/replay", s.replay)
	s.mux.HandleFunc("/v1/agents", s.agents)
	s.mux.HandleFunc("/v1/agents/", s.agentResource)
	s.mux.HandleFunc("/v1/sessions", s.sessions)
	s.mux.HandleFunc("/v1/sessions/", s.sessionResource)
	s.mux.HandleFunc("/v1/workspace/", s.workspaceResource)
}
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "product": "RSCH CLI"})
}
func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	result := map[string]any{"product": "RSCH CLI", "goals": len(s.store.Goals()), "tasks": len(s.store.Tasks())}
	if s.db != nil {
		result["agents"] = count(s.db, "workflow_agents")
		result["sessions"] = count(s.db, "sessions")
		result["events"] = count(s.db, "event_log")
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request, action string) bool {
	if s.mutationPolicy == nil {
		return true
	}
	// Approval is an authorization decision, not a property an HTTP caller can
	// assert. Keep the legacy header out of policy callbacks so a custom policy
	// cannot accidentally turn a user-controlled value into an approval grant.
	policyRequest := r.Clone(r.Context())
	policyRequest.Header = r.Header.Clone()
	policyRequest.Header.Del(approvalHeader)
	if err := s.mutationPolicy(policyRequest, action); err != nil {
		writeError(w, http.StatusForbidden, err)
		return false
	}
	return true
}

func count(db *sql.DB, table string) int {
	var value int
	if db != nil {
		_ = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&value)
	}
	return value
}

func (s *Server) workspaceResource(w http.ResponseWriter, r *http.Request) {
	if s.root == "" {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("workspace inspection is not configured"))
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/workspace/"), "/")
	if strings.HasPrefix(name, "jobs/") {
		s.backgroundJobResource(w, r, strings.TrimPrefix(name, "jobs/"))
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	switch name {
	case "audit":
		log, err := audit.New(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items, err := log.List(100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "memory":
		data, err := os.ReadFile(filepath.Join(s.root, ".rsch", "memory", "entries.json"))
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "steering":
		value, err := steering.Load(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "release":
		data, err := os.ReadFile(filepath.Join(s.root, ".rsch", "release", "matrix.json"))
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"evidence": []any{}})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "jobs":
		manager, err := s.newBackgroundManager()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items, err := manager.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "cost":
		result := map[string]any{
			"sessions":      0,
			"total":         0.0,
			"estimatedCost": 0.0,
			"inputTokens":   int64(0),
			"outputTokens":  int64(0),
			"durationNs":    int64(0),
			"models":        map[string]int{},
			"providers":     map[string]int{},
			"taskCosts":     map[string]float64{},
			"modelEvidence": false,
			"usageEvidence": false,
		}
		if s.db != nil {
			var total float64
			var sessions int
			var inputTokens, outputTokens int64
			if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*), COALESCE(SUM(cost), 0), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0) FROM sessions`).Scan(&sessions, &total, &inputTokens, &outputTokens); err == nil {
				result["sessions"], result["total"], result["estimatedCost"], result["inputTokens"], result["outputTokens"] = sessions, total, total, inputTokens, outputTokens
			}
			models := map[string]int{}
			if rows, err := s.db.Query("SELECT model, COUNT(*) FROM messages WHERE role = 'assistant' AND model IS NOT NULL GROUP BY model"); err == nil {
				for rows.Next() {
					var model string
					var count int
					if rows.Scan(&model, &count) == nil {
						models[model] = count
					}
				}
				_ = rows.Close()
				result["modelEvidence"] = true
			}
			result["models"] = models
			providers := map[string]int{}
			taskCosts := map[string]float64{}
			var durationNs int64
			usageModels := map[string]int{}
			var usageInputTokens, usageOutputTokens int64
			var usageCost float64
			usageRows := 0
			if rows, err := s.db.QueryContext(r.Context(), `SELECT provider, model, task_id, input_tokens, output_tokens, cost, duration_ns FROM model_usage`); err == nil {
				for rows.Next() {
					var provider, model, taskID string
					var inputTokens, outputTokens int64
					var duration int64
					var decimalCost float64
					if rows.Scan(&provider, &model, &taskID, &inputTokens, &outputTokens, &decimalCost, &duration) != nil {
						continue
					}
					usageRows++
					usageInputTokens += inputTokens
					usageOutputTokens += outputTokens
					usageCost += decimalCost
					if provider != "" {
						providers[provider]++
					}
					if model != "" {
						usageModels[model]++
					}
					if taskID != "" {
						taskCosts[taskID] += decimalCost
					}
					durationNs += duration
				}
				_ = rows.Close()
				result["usageEvidence"] = true
			}
			if usageRows > 0 {
				result["inputTokens"], result["outputTokens"] = usageInputTokens, usageOutputTokens
				result["usageCost"] = usageCost
				if total == 0 && usageCost != 0 {
					result["total"], result["estimatedCost"] = usageCost, usageCost
				}
				models = usageModels
			}
			result["providers"], result["taskCosts"], result["durationNs"] = providers, taskCosts, durationNs
			result["models"] = models
		}
		result["budget"] = s.workspaceCostBudget(result["total"].(float64), result["taskCosts"].(map[string]float64))
		writeJSON(w, http.StatusOK, result)
	case "agents":
		if s.db == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		items, err := orchestration.NewAgentStore(s.db).List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "messages":
		if s.log == nil {
			writeError(w, http.StatusNotImplemented, fmt.Errorf("event replay storage is not configured"))
			return
		}
		items, err := s.replayEvents(r.Context(), eventFilter{
			GoalID:    strings.TrimSpace(r.URL.Query().Get("goalId")),
			TaskID:    strings.TrimSpace(r.URL.Query().Get("taskId")),
			SessionID: strings.TrimSpace(r.URL.Query().Get("sessionId")),
			Type:      events.AgentMessage,
		}, 10000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "replay":
		if s.log == nil {
			writeError(w, http.StatusNotImplemented, fmt.Errorf("event replay storage is not configured"))
			return
		}
		detail, err := s.buildReplayDetail(r.Context(), eventFilter{
			GoalID:    strings.TrimSpace(r.URL.Query().Get("goalId")),
			TaskID:    strings.TrimSpace(r.URL.Query().Get("taskId")),
			SessionID: strings.TrimSpace(r.URL.Query().Get("sessionId")),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	case "browser":
		endpoint := os.Getenv("RSCH_WEBDRIVER_URL")
		writeJSON(w, http.StatusOK, map[string]any{"configured": endpoint != "", "endpoint": endpoint != ""})
	case "notifications":
		provider, err := notification.NewFile(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items, err := provider.List(100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "dependencies":
		report, err := dependencies.Scan(r.Context(), s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	case "security":
		index, err := intelligence.New(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := index.Build(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		findings := make([]security.Finding, 0)
		for _, file := range index.Files() {
			items, scanErr := security.ScanFile(filepath.Join(s.root, filepath.FromSlash(file.Path)))
			if scanErr != nil {
				writeError(w, http.StatusInternalServerError, scanErr)
				return
			}
			findings = append(findings, items...)
		}
		externalNames := []string{}
		if raw := strings.TrimSpace(r.URL.Query().Get("external")); raw != "" {
			for _, name := range strings.Split(raw, ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					externalNames = append(externalNames, trimmed)
				}
			}
		}
		if len(externalNames) > 0 && !s.authorizeMutation(w, r, "security.external") {
			return
		}
		external, err := security.RunExternal(r.Context(), s.root, externalNames)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"localFindings": findings, "external": external})
	case "evidence":
		store, err := evidence.New(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			parsed, parseErr := strconv.Atoi(rawLimit)
			if parseErr != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("limit must be a non-negative integer"))
				return
			}
			limit = parsed
		}
		items, err := store.List(limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
			filtered := items[:0]
			for _, item := range items {
				if item.Kind == kind {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		writeJSON(w, http.StatusOK, items)
	case "evidence/links":
		store, err := evidence.New(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		limit := 100
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			parsed, parseErr := strconv.Atoi(rawLimit)
			if parseErr != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, fmt.Errorf("limit must be a non-negative integer"))
				return
			}
			limit = parsed
		}
		links, err := store.ListLinks(evidence.LinkQuery{
			EvidenceID:    strings.TrimSpace(r.URL.Query().Get("evidenceId")),
			GoalID:        strings.TrimSpace(r.URL.Query().Get("goalId")),
			TaskID:        strings.TrimSpace(r.URL.Query().Get("taskId")),
			RequirementID: strings.TrimSpace(r.URL.Query().Get("requirementId")),
		}, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, links)
	case "requirements":
		data, err := os.ReadFile(filepath.Join(s.root, ".rsch", "requirements", "audit.json"))
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"sections": []any{}, "available": false})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "confidence":
		path := filepath.Join(s.root, ".rsch", "confidence", "latest.json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			computed, buildErr := confidence.Build(s.root)
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, buildErr)
				return
			}
			writeJSON(w, http.StatusOK, computed)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	case "database":
		if s.db == nil {
			writeError(w, http.StatusNotImplemented, fmt.Errorf("database inspection is not configured"))
			return
		}
		schema, err := dbintel.InspectWorkspace(r.Context(), s.db, s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, schema)
	case "architecture", "context":
		index, err := intelligence.New(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := index.Build(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if name == "architecture" {
			writeJSON(w, http.StatusOK, map[string]any{"files": index.Files(), "symbols": index.Symbols()})
			return
		}
		query := r.URL.Query().Get("q")
		if query == "" {
			query = "repository architecture"
		}
		budget, _ := strconv.Atoi(r.URL.Query().Get("budget"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := index.BuildContext(r.Context(), query, budget, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "performance":
		budget, err := performance.LoadProjectBudget(s.root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		databaseReport := performance.InspectDatabase(s.db)
		bundle := performance.InspectBundle(r.Context(), s.root)
		budgetChecks := []performance.BudgetCheck{
			performance.EvaluateBudget("build", budget.BuildTimeMS, 0, false, "build measurement requires an explicit performance check"),
			performance.EvaluateBudget("startup", budget.StartupMS, 0, false, "startup measurement requires an explicit performance check"),
			performance.EvaluateBudget("bundle", budget.BundleBytes, bundle.Bytes, bundle.Observed, bundle.Reason),
			performance.EvaluateBudget("memory", budget.MemoryBytes, int64(stats.HeapAlloc), true, "runtime heap allocation observed"),
			performance.EvaluateBudget("query-count", budget.QueryCount, databaseReport.QueryCount, databaseReport.QueryCountKnown, databaseReport.Reason),
			performance.EvaluateBudget("latency", budget.LatencyMS, 0, false, "request latency requires an explicitly configured request target"),
		}
		value := map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH, "goRoutines": runtime.NumGoroutine(), "memory": map[string]uint64{"alloc": stats.Alloc, "sys": stats.Sys, "heap": stats.HeapAlloc}, "budget": budget, "budgetChecks": budgetChecks, "bundle": bundle, "budgetComplete": performance.BudgetChecksComplete(budgetChecks), "io": performance.InspectIO(r.Context(), s.root), "system": performance.InspectSystem(s.root)}
		value["database"] = databaseReport
		writeJSON(w, http.StatusOK, value)
	case "capabilities":
		writeJSON(w, http.StatusOK, map[string]any{"sandbox": sandbox.Probe(r.Context(), s.root), "platform": runtime.GOOS + "/" + runtime.GOARCH})
	case "team":
		resources := []map[string]any{}
		for _, resource := range []string{"agents", "skills", "steering", "hooks", "policies", "specs", "memory"} {
			path := filepath.Join(s.root, ".rsch", resource)
			count, available := workspaceFileCount(path)
			resources = append(resources, map[string]any{"name": resource, "path": filepath.ToSlash(filepath.Join(".rsch", resource)), "available": available, "files": count})
		}
		writeJSON(w, http.StatusOK, map[string]any{"root": s.root, "resources": resources})
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("workspace resource %q not found", name))
	}
}

func readWorkspaceJSON(root string) ([]any, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item any
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func workspaceFileCount(root string) (int, bool) {
	count, available := 0, false
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		available = true
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	return count, available
}

func (s *Server) workspaceCostBudget(total float64, taskCosts map[string]float64) map[string]any {
	result := map[string]any{
		"configured": false,
		"task":       map[string]any{"configured": false, "checks": map[string]any{}},
		"session":    map[string]any{"configured": false, "actual": total},
	}
	loaded := config.Get()
	if loaded == nil {
		return result
	}
	taskChecks := map[string]any{}
	taskPassed := true
	for taskID, actual := range taskCosts {
		passed := loaded.Budget.Task <= 0 || actual <= loaded.Budget.Task
		if !passed {
			taskPassed = false
		}
		taskChecks[taskID] = map[string]any{"actual": actual, "limit": loaded.Budget.Task, "configured": loaded.Budget.Task > 0, "passed": passed}
	}
	sessionPassed := loaded.Budget.Session <= 0 || total <= loaded.Budget.Session
	result["configured"] = loaded.Budget.Task > 0 || loaded.Budget.Session > 0
	result["task"] = map[string]any{"configured": loaded.Budget.Task > 0, "limit": loaded.Budget.Task, "checks": taskChecks, "passed": taskPassed}
	result["session"] = map[string]any{"configured": loaded.Budget.Session > 0, "limit": loaded.Budget.Session, "actual": total, "passed": sessionPassed}
	result["passed"] = taskPassed && sessionPassed
	return result
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("agent persistence is not configured"))
		return
	}
	store := orchestration.NewAgentStore(s.db)
	switch r.Method {
	case http.MethodGet:
		items, err := store.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !s.authorizeMutation(w, r, "agent.create") {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()
		var agent orchestration.Agent
		if err := decodeJSONBody(r.Body, &agent); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(agent.Role) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("agent role is required"))
			return
		}
		if agent.ID == "" {
			generated := orchestration.NewAgent(agent.Role, agent.TaskID)
			agent.ID = generated.ID
			agent.Status = generated.Status
		}
		if agent.Status == "" {
			agent.Status = orchestration.AgentCreated
		}
		if agent.Status != orchestration.AgentCreated {
			writeError(w, http.StatusBadRequest, fmt.Errorf("new agents must start in created state"))
			return
		}
		if loaded := config.Get(); loaded != nil && loaded.Resources.MaxAgents > 0 {
			if err := store.EnsureCapacity(r.Context(), loaded.Resources.MaxAgents, agent.ID, agent.Status); err != nil {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
		}
		if err := store.Save(r.Context(), agent); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		persisted, listErr := store.List(r.Context())
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		for _, item := range persisted {
			if item.ID == agent.ID {
				agent = item
				break
			}
		}
		event := events.New(events.AgentCreated, map[string]any{"role": agent.Role, "taskId": agent.TaskID})
		event.Actor, event.TaskID = agent.ID, agent.TaskID
		s.publish(r.Context(), event)
		writeJSON(w, http.StatusCreated, agent)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("session persistence is not configured"))
		return
	}
	service := session.NewServiceWithDB(rschdb.New(s.db), s.db)
	switch r.Method {
	case http.MethodGet:
		items, err := service.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !s.authorizeMutation(w, r, "session.create") {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()
		var request struct {
			Title string `json:"title"`
		}
		if err := decodeJSONBody(r.Body, &request); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item, err := service.Create(r.Context(), request.Title)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		event := events.New(events.SessionStarted, map[string]any{"title": item.Title})
		event.SessionID = item.ID
		s.publish(r.Context(), event)
		writeJSON(w, http.StatusCreated, item)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

func (s *Server) sessionResource(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("session persistence is not configured"))
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, fmt.Errorf("session resource not found"))
		return
	}
	service := session.NewServiceWithDB(rschdb.New(s.db), s.db)
	item, err := service.Get(r.Context(), parts[0])
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if len(parts) == 2 {
		if parts[1] != "usage" || r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
			return
		}
		usage, usageErr := service.Usage(r.Context(), item.ID)
		if usageErr != nil {
			writeError(w, http.StatusInternalServerError, usageErr)
			return
		}
		writeJSON(w, http.StatusOK, usage)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		if !s.authorizeMutation(w, r, "session.delete") {
			return
		}
		if err := service.Delete(r.Context(), item.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		event := events.New(events.SessionDeleted, map[string]any{"deleted": true})
		event.SessionID = item.ID
		s.publish(r.Context(), event)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

func (s *Server) agentResource(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("agent persistence is not configured"))
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/agents/"), "/"), "/")
	if len(parts) == 1 && parts[0] == "recover" && r.Method == http.MethodPost {
		if !s.authorizeMutation(w, r, "agent.recover") {
			return
		}
		store := orchestration.NewAgentStore(s.db)
		items, err := store.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		recovered := make([]orchestration.Agent, 0)
		for _, agent := range items {
			if agent.Status != orchestration.AgentRunning {
				continue
			}
			agent.Status = orchestration.AgentPaused
			if err := store.Save(r.Context(), agent); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			recovered = append(recovered, agent)
			event := events.New(events.AgentPaused, map[string]any{"status": agent.Status, "recovered": true})
			event.Actor, event.TaskID = agent.ID, agent.TaskID
			s.publish(r.Context(), event)
		}
		writeJSON(w, http.StatusOK, recovered)
		return
	}
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, fmt.Errorf("agent resource not found"))
		return
	}
	store := orchestration.NewAgentStore(s.db)
	items, err := store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	index := -1
	for i := range items {
		if items[i].ID == parts[0] {
			index = i
			break
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("agent %q not found", parts[0]))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, items[index])
		return
	}
	if len(parts) != 2 || parts[1] != "transition" || r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	if !s.authorizeMutation(w, r, "agent.transition") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var request struct {
		Status orchestration.AgentStatus `json:"status"`
	}
	if err := decodeJSONBody(r.Body, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agent := items[index]
	if err := agent.Transition(request.Status); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if loaded := config.Get(); loaded != nil && loaded.Resources.MaxAgents > 0 {
		if err := store.EnsureCapacity(r.Context(), loaded.Resources.MaxAgents, agent.ID, agent.Status); err != nil {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
	}
	if err := store.Save(r.Context(), agent); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	event := events.New(agentEventType(request.Status), map[string]any{"status": request.Status})
	event.Actor, event.TaskID = agent.ID, agent.TaskID
	s.publish(r.Context(), event)
	writeJSON(w, http.StatusOK, agent)
}

func agentEventType(status orchestration.AgentStatus) events.Type {
	switch status {
	case orchestration.AgentPaused:
		return events.AgentPaused
	case orchestration.AgentResumed:
		return events.AgentResumed
	case orchestration.AgentCompleted:
		return events.AgentCompleted
	case orchestration.AgentFailed:
		return events.AgentFailed
	case orchestration.AgentCanceled:
		return events.AgentCanceled
	default:
		return events.AgentStarted
	}
}

func (s *Server) backgroundJobResource(w http.ResponseWriter, r *http.Request, resource string) {
	parts := strings.Split(strings.Trim(resource, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, fmt.Errorf("background job resource not found"))
		return
	}
	manager, err := s.newBackgroundManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		job, log, err := manager.ReadLog(id, 0)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"job": job, "log": log})
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	if !s.authorizeMutation(w, r, "background."+parts[1]) {
		return
	}
	var job background.Job
	switch parts[1] {
	case "stop":
		err = manager.Stop(id)
	case "recover":
		job, err = manager.Recover(id)
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("background action %q not found", parts[1]))
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if job.ID == "" {
		job, err = manager.Recover(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) newBackgroundManager() (*background.Manager, error) {
	limit := 2
	settings := notification.Config{}
	if loaded := config.Get(); loaded != nil {
		if loaded.Resources.MaxBackgroundJobs > 0 {
			limit = loaded.Resources.MaxBackgroundJobs
		}
		settings = notification.Config{
			WebhookURL:      loaded.Notifications.WebhookURL,
			Command:         loaded.Notifications.Command,
			Args:            loaded.Notifications.Args,
			Timeout:         time.Duration(loaded.Notifications.TimeoutMS) * time.Millisecond,
			AllowedHosts:    loaded.Network.AllowedHosts,
			AllowPrivate:    loaded.Network.AllowPrivate,
			AllowedCommands: loaded.Policy.CommandAllow,
		}
	}
	return background.NewManagerWithNotificationsAndLimit(s.root, settings, limit)
}

type eventFilter struct {
	GoalID    string
	TaskID    string
	SessionID string
	Type      events.Type
}

func (s *Server) replayEvents(ctx context.Context, filter eventFilter, limit int) ([]events.Event, error) {
	return s.replayEventsAfter(ctx, 0, filter, limit)
}

func (s *Server) replayEventsAfter(ctx context.Context, after int64, filter eventFilter, limit int) ([]events.Event, error) {
	// Match the durable log's existing result limit while applying it to
	// matching events, rather than discarding matches beyond the first page.
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	const batchSize = 1000
	out := make([]events.Event, 0)
	for {
		items, err := s.log.Replay(ctx, after, batchSize)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			after = item.Sequence
			if filter.GoalID != "" && item.GoalID != filter.GoalID || filter.TaskID != "" && item.TaskID != filter.TaskID || filter.SessionID != "" && item.SessionID != filter.SessionID || filter.Type != "" && item.Type != filter.Type {
				continue
			}
			out = append(out, item)
			if len(out) == limit {
				return out, nil
			}
		}
		if len(items) < batchSize {
			return out, nil
		}
	}
}

type replayDetail struct {
	GoalID       string                `json:"goalId,omitempty"`
	TaskID       string                `json:"taskId,omitempty"`
	Goal         *engineering.Goal     `json:"goal,omitempty"`
	Tasks        []engineering.Task    `json:"tasks"`
	Events       []events.Event        `json:"events"`
	Agents       []orchestration.Agent `json:"agents"`
	Messages     []events.Event        `json:"messages"`
	ToolActions  []events.Event        `json:"toolActions"`
	Failures     []events.Event        `json:"failures"`
	Repairs      []events.Event        `json:"repairs"`
	Verification []events.Event        `json:"verification"`
	FinalResult  *events.Event         `json:"finalResult,omitempty"`
}

func (s *Server) buildReplayDetail(ctx context.Context, filter eventFilter) (replayDetail, error) {
	items, err := s.replayEvents(ctx, filter, 10000)
	if err != nil {
		return replayDetail{}, err
	}
	detail := replayDetail{GoalID: filter.GoalID, TaskID: filter.TaskID, Tasks: []engineering.Task{}, Events: items, Messages: []events.Event{}, ToolActions: []events.Event{}, Failures: []events.Event{}, Repairs: []events.Event{}, Verification: []events.Event{}}
	for i := range items {
		item := items[i]
		if detail.GoalID == "" {
			detail.GoalID = item.GoalID
		}
		if detail.TaskID == "" {
			detail.TaskID = item.TaskID
		}
		switch item.Type {
		case events.AgentMessage:
			detail.Messages = append(detail.Messages, item)
		case events.ToolBefore, events.ToolAfter, events.ToolFailed:
			detail.ToolActions = append(detail.ToolActions, item)
			if item.Type == events.ToolFailed {
				detail.Failures = append(detail.Failures, item)
			}
		case events.TaskFailed, events.AgentFailed, events.ModelFailed:
			detail.Failures = append(detail.Failures, item)
		case events.TaskStarted, events.TaskCompleted, events.AgentResumed:
			if item.Type == events.AgentResumed || item.Type == events.TaskStarted {
				detail.Repairs = append(detail.Repairs, item)
			}
		case events.TestStarted, events.TestPassed, events.TestFailed, events.SecurityScanStarted, events.SecurityScanCompleted:
			detail.Verification = append(detail.Verification, item)
		}
	}
	if len(items) > 0 {
		detail.FinalResult = &items[len(items)-1]
	}
	for _, goal := range s.store.Goals() {
		if detail.GoalID != "" && goal.ID == detail.GoalID {
			goalCopy := goal
			detail.Goal = &goalCopy
			break
		}
	}
	for _, task := range s.store.Tasks() {
		if detail.TaskID != "" && task.ID != detail.TaskID || detail.GoalID != "" && task.GoalID != detail.GoalID {
			continue
		}
		detail.Tasks = append(detail.Tasks, task)
	}
	if s.db != nil {
		agents, listErr := orchestration.NewAgentStore(s.db).List(ctx)
		if listErr != nil {
			return replayDetail{}, listErr
		}
		for _, agent := range agents {
			if detail.TaskID == "" || agent.TaskID == detail.TaskID {
				detail.Agents = append(detail.Agents, agent)
			}
		}
	}
	if detail.Agents == nil {
		detail.Agents = []orchestration.Agent{}
	}
	return detail, nil
}

func (s *Server) goals(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.Goals())
	case http.MethodPost:
		if !s.authorizeMutation(w, r, "goal.create") {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()
		var goal engineering.Goal
		if err := decodeJSONBody(r.Body, &goal); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		goal = engineering.PrepareGoal(goal)
		if err := s.store.PutGoal(goal); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		event := events.New(events.GoalCreated, map[string]any{"title": goal.Title})
		event.GoalID = goal.ID
		s.publish(r.Context(), event)
		writeJSON(w, http.StatusCreated, goal)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

// tasks exposes a client-independent task collection so API consumers do not
// need to mirror the nested goal URL shape used by the original CLI surface.
func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if !s.lockTaskMutation(w, r) {
			return
		}
		defer s.taskMutationMu.Unlock()
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.LoadTasks(r.Context(), strings.TrimSpace(r.URL.Query().Get("goalId")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !s.authorizeMutation(w, r, "task.create") {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		defer r.Body.Close()
		var task engineering.Task
		if err := decodeJSONBody(r.Body, &task); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		goalID := strings.TrimSpace(task.GoalID)
		if goalID == "" {
			goalID = strings.TrimSpace(r.URL.Query().Get("goalId"))
		}
		if goalID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("task goalId is required"))
			return
		}
		if task.ID == "" {
			task.ID = uuid.NewString()
		}
		task.GoalID = goalID
		if task.Status == "" {
			task.Status = engineering.StatusPending
		}
		event := events.New(events.TaskCreated, map[string]any{"taskId": task.ID})
		event.GoalID, event.TaskID = goalID, task.ID
		items, err := s.mutateTaskCollection(r.Context(), goalID, func(items []engineering.Task) ([]engineering.Task, events.Event, error) {
			for _, existing := range items {
				if existing.ID == task.ID {
					return nil, events.Event{}, &taskMutationError{http.StatusConflict, fmt.Errorf("task %q already exists", task.ID)}
				}
			}
			return append(items, task), event, nil
		})
		if err != nil {
			writeTaskMutationError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, taskFromCollection(items, task.ID))
	case http.MethodPut:
		if !s.authorizeMutation(w, r, "tasks.replace") {
			return
		}
		goalID := strings.TrimSpace(r.URL.Query().Get("goalId"))
		if goalID == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("goalId is required for task collection replacement"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		defer r.Body.Close()
		var items []engineering.Task
		if err := decodeJSONBody(r.Body, &items); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		for i := range items {
			if items[i].ID == "" {
				items[i].ID = uuid.NewString()
			}
			items[i].GoalID = goalID
		}
		event := events.New(events.TasksUpdated, map[string]any{"resource": "tasks", "count": len(items)})
		event.GoalID = goalID
		items, err := s.replaceTaskCollection(r.Context(), goalID, items, event)
		if err != nil {
			writeTaskMutationError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

func (s *Server) taskResource(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if !s.lockTaskMutation(w, r) {
			return
		}
		defer s.taskMutationMu.Unlock()
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		writeError(w, http.StatusNotFound, fmt.Errorf("task resource not found"))
		return
	}
	items, err := s.store.LoadTasks(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	index := -1
	for i := range items {
		if items[i].ID == parts[0] {
			index = i
			break
		}
	}
	if index < 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("task %q not found", parts[0]))
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, items[index])
		case http.MethodPut:
			if !s.authorizeMutation(w, r, "task.update") {
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
			defer r.Body.Close()
			var updated engineering.Task
			if err := decodeJSONBody(r.Body, &updated); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			taskID, goalID := items[index].ID, items[index].GoalID
			event := events.New(events.TasksUpdated, map[string]any{"taskId": taskID, "updated": true})
			event.GoalID, event.TaskID = goalID, taskID
			persisted, err := s.mutateTaskCollection(r.Context(), goalID, func(current []engineering.Task) ([]engineering.Task, events.Event, error) {
				for i := range current {
					if current[i].ID != taskID {
						continue
					}
					replacement := updated
					replacement.ID, replacement.GoalID = taskID, goalID
					if replacement.Status != "" && replacement.Status != current[i].Status {
						return nil, events.Event{}, &taskMutationError{http.StatusConflict, fmt.Errorf("task status changes require the transition endpoint")}
					}
					if replacement.Status == "" {
						replacement.Status = current[i].Status
					}
					current[i] = replacement
					return current, event, nil
				}
				return nil, events.Event{}, &taskMutationError{http.StatusNotFound, fmt.Errorf("task %q not found", taskID)}
			})
			if err != nil {
				writeTaskMutationError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, taskFromCollection(persisted, taskID))
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		}
		return
	}
	if parts[1] != "transition" || r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, PUT")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	if !s.authorizeMutation(w, r, "task.transition") {
		return
	}
	s.transitionTask(w, r, items[index].GoalID, items[index].ID)
}

func (s *Server) goalResource(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/goals/"), "/"), "/")
	if r.Method == http.MethodPost && len(parts) >= 2 && parts[1] == "tasks" {
		if !s.lockTaskMutation(w, r) {
			return
		}
		defer s.taskMutationMu.Unlock()
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "transition" && r.Method == http.MethodPost {
		if !s.authorizeMutation(w, r, "goal.transition") {
			return
		}
		s.transitionGoal(w, r, parts[0])
		return
	}
	if len(parts) == 4 && parts[0] != "" && parts[1] == "tasks" && parts[2] != "" && parts[3] == "transition" && r.Method == http.MethodPost {
		if !s.authorizeMutation(w, r, "task.transition") {
			return
		}
		s.transitionTask(w, r, parts[0], parts[2])
		return
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "tasks" && parts[2] != "" && r.Method == http.MethodGet {
		items, err := s.store.LoadTasks(r.Context(), parts[0])
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, item := range items {
			if item.ID == parts[2] {
				writeJSON(w, http.StatusOK, item)
				return
			}
		}
		writeError(w, http.StatusNotFound, fmt.Errorf("task %q not found in goal %q", parts[2], parts[0]))
		return
	}
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
		for _, goal := range s.store.Goals() {
			if goal.ID == parts[0] {
				writeJSON(w, http.StatusOK, goal)
				return
			}
		}
		writeError(w, http.StatusNotFound, fmt.Errorf("goal %q not found", parts[0]))
		return
	}
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("goal resource not found"))
		return
	}
	goalID, resource := parts[0], parts[1]
	switch resource {
	case "requirements":
		switch r.Method {
		case http.MethodGet:
			items, err := s.store.Requirements(r.Context(), goalID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			if !s.authorizeMutation(w, r, "requirement.create") {
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			defer r.Body.Close()
			var item engineering.Requirement
			if err := decodeJSONBody(r.Body, &item); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			item.GoalID = goalID
			if err := s.store.SaveRequirement(r.Context(), item); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			event := events.New(events.RequirementUpdated, map[string]any{"resource": "requirement", "id": item.ID})
			event.GoalID = goalID
			s.publish(r.Context(), event)
			writeJSON(w, http.StatusCreated, item)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		}
	case "tasks":
		switch r.Method {
		case http.MethodGet:
			items, err := s.store.LoadTasks(r.Context(), goalID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, items)
		case http.MethodPost:
			if !s.authorizeMutation(w, r, "tasks.replace") {
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
			defer r.Body.Close()
			var items []engineering.Task
			if err := decodeJSONBody(r.Body, &items); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			for i := range items {
				items[i].GoalID = goalID
			}
			event := events.New(events.TasksUpdated, map[string]any{"resource": "tasks", "count": len(items)})
			event.GoalID = goalID
			items, err := s.replaceTaskCollection(r.Context(), goalID, items, event)
			if err != nil {
				writeTaskMutationError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, items)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		}
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("goal resource %q not found", resource))
	}
}

func (s *Server) transitionGoal(w http.ResponseWriter, r *http.Request, goalID string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var request struct {
		Status engineering.Status `json:"status"`
	}
	if err := decodeJSONBody(r.Body, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, goal := range s.store.Goals() {
		if goal.ID != goalID {
			continue
		}
		if err := engineering.TransitionGoal(goal.Status, request.Status); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		goal.Status = request.Status
		if err := s.store.PutGoal(goal); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		kind := events.GoalStarted
		if request.Status == engineering.StatusCompleted {
			kind = events.GoalCompleted
		} else if request.Status == engineering.StatusFailed {
			kind = events.GoalFailed
		}
		event := events.New(kind, map[string]any{"status": request.Status})
		event.GoalID = goalID
		s.publish(r.Context(), event)
		writeJSON(w, http.StatusOK, goal)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("goal %q not found", goalID))
}

func (s *Server) transitionTask(w http.ResponseWriter, r *http.Request, goalID, taskID string) {
	items, err := s.store.LoadTasks(r.Context(), goalID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	found := false
	for _, item := range items {
		if item.ID == taskID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Errorf("task %q is not part of goal %q", taskID, goalID))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var request struct {
		Status engineering.Status `json:"status"`
	}
	if err := decodeJSONBody(r.Body, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if task, handled, err := s.transitionTaskAtomically(r.Context(), goalID, taskID, request.Status); handled {
		if err != nil {
			writeTaskMutationError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}
	task, err := s.store.TransitionTask(r.Context(), taskID, request.Status)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusConflict, err)
		return
	}
	if task.GoalID != goalID {
		writeError(w, http.StatusNotFound, fmt.Errorf("task %q is not part of goal %q", taskID, goalID))
		return
	}
	event := events.New(taskEventType(request.Status), map[string]any{"status": request.Status})
	event.GoalID, event.TaskID = goalID, taskID
	s.publish(r.Context(), event)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	if s.log == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("event replay storage is not configured"))
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("after must be a non-negative event sequence"))
			return
		}
		after = parsed
	}
	limit := 1000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	filter := eventFilter{GoalID: strings.TrimSpace(r.URL.Query().Get("goalId")), TaskID: strings.TrimSpace(r.URL.Query().Get("taskId")), SessionID: strings.TrimSpace(r.URL.Query().Get("sessionId")), Type: events.Type(strings.TrimSpace(r.URL.Query().Get("type")))}
	items, err := s.replayEventsAfter(r.Context(), after, filter, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (s *Server) lockTaskMutation(w http.ResponseWriter, r *http.Request) bool {
	s.taskMutationMu.Lock()
	if err := r.Context().Err(); err != nil {
		s.taskMutationMu.Unlock()
		writeError(w, http.StatusRequestTimeout, err)
		return false
	}
	return true
}

func (s *Server) publish(ctx context.Context, event events.Event) {
	// The mutation has already committed. Report delivery failures separately
	// rather than returning a response that would encourage retrying it.
	eventID := security.RedactText(event.ID)
	eventType := security.RedactText(string(event.Type))
	if s.log != nil {
		if err := s.log.AppendEvent(ctx, &event); err != nil {
			logging.Error("Server event persistence failed after mutation", "eventId", eventID, "eventType", eventType, "error", security.RedactText(err.Error()))
		}
	}
	if !s.bus.PublishContext(ctx, event) {
		logging.Error("Server event delivery failed after mutation", "eventId", eventID, "eventType", eventType)
	}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming is unavailable"))
		return
	}
	// The command server has a finite WriteTimeout for ordinary responses.
	// SSE is intentionally long lived, so clear that connection deadline while
	// retaining request cancellation and write-error handling below.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	var after int64
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after"))
	// EventSource retains its URL when reconnecting and sends the last received
	// sequence in this header. Prefer it over the URL's original catch-up cursor.
	if s.log != nil && strings.TrimSpace(r.Header.Get("Last-Event-ID")) != "" {
		rawAfter = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("after must be a non-negative event sequence"))
			return
		}
		after = parsed
	}
	// Subscribe before reading the durable catch-up window. Events arriving
	// during replay remain buffered. The durable sequence also lets us discard
	// events already replayed without retaining the entire history in memory.
	ch, cancel := s.bus.Subscribe()
	defer cancel()
	const replayBatchSize = 1000
	var items []events.Event
	replaying := rawAfter != "" && s.log != nil
	if replaying {
		var replayErr error
		items, replayErr = s.log.Replay(r.Context(), after, replayBatchSize)
		if replayErr != nil {
			writeError(w, http.StatusInternalServerError, replayErr)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Establish even an idle stream promptly, so clients can finish connecting.
	flusher.Flush()
	for replaying {
		for _, event := range items {
			if err := writeSSE(w, event); err != nil {
				return
			}
			after = event.Sequence
		}
		flusher.Flush()
		if len(items) < replayBatchSize {
			break
		}
		var replayErr error
		items, replayErr = s.log.Replay(r.Context(), after, replayBatchSize)
		if replayErr != nil {
			// Headers have already been sent. Close the stream so a reconnect
			// can resume from the last successfully delivered event.
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if event.Sequence > 0 && event.Sequence <= after {
				continue
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event events.Event) error {
	data, err := event.JSON()
	if err != nil {
		return err
	}
	streamID := event.ID
	if event.Sequence > 0 {
		streamID = strconv.FormatInt(event.Sequence, 10)
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", streamID, event.Type, data)
	return err
}

// decodeJSONBody consumes the entire bounded request body before a mutation.
// Decoding only the first value would accept trailing garbage, extra values,
// and oversized suffixes. A null document must not clear a task collection.
func decodeJSONBody(body io.Reader, target any) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("request body must not be null")
	}
	return json.Unmarshal(data, target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(err.Error())})
}
