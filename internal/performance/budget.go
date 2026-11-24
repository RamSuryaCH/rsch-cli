// Package performance provides concurrency-safe resource budgets and usage
// accounting for agents, tools, verification, and model routing.
package performance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/opencode-ai/opencode/internal/process"
)

var ErrBudgetExceeded = errors.New("performance budget exceeded")

type Budget struct {
	MaxDuration time.Duration `json:"maxDuration,omitempty"`
	MaxCalls    int64         `json:"maxCalls,omitempty"`
	MaxBytes    int64         `json:"maxBytes,omitempty"`
	MaxCost     float64       `json:"maxCost,omitempty"`
}

// ProjectBudget describes only limits explicitly supplied by a project. Zero
// means "not configured" and is never treated as an arbitrary failure.
type ProjectBudget struct {
	BuildTimeMS int64 `json:"buildTimeMs,omitempty"`
	StartupMS   int64 `json:"startupMs,omitempty"`
	BundleBytes int64 `json:"bundleBytes,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	QueryCount  int64 `json:"queryCount,omitempty"`
	LatencyMS   int64 `json:"latencyMs,omitempty"`
}

type BudgetCheck struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Observed   bool   `json:"observed"`
	Budget     int64  `json:"budget,omitempty"`
	Actual     int64  `json:"actual,omitempty"`
	Within     *bool  `json:"within,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type BundleReport struct {
	Observed  bool   `json:"observed"`
	Path      string `json:"path,omitempty"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason,omitempty"`
}

type Measurement struct {
	Kind     string        `json:"kind"`
	Duration time.Duration `json:"duration"`
	Bytes    int64         `json:"bytes,omitempty"`
	Observed bool          `json:"observed"`
	Budget   int64         `json:"budget,omitempty"`
	Within   *bool         `json:"within,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type IOReport struct {
	Observed  bool          `json:"observed"`
	Duration  time.Duration `json:"duration"`
	Files     int64         `json:"files"`
	Bytes     int64         `json:"bytes"`
	Truncated bool          `json:"truncated"`
	Error     string        `json:"error,omitempty"`
}

type DatabaseReport struct {
	Available       bool          `json:"available"`
	OpenConnections int           `json:"openConnections,omitempty"`
	InUse           int           `json:"inUse,omitempty"`
	Idle            int           `json:"idle,omitempty"`
	WaitCount       int64         `json:"waitCount,omitempty"`
	WaitDuration    time.Duration `json:"waitDuration,omitempty"`
	QueryCount      int64         `json:"queryCount,omitempty"`
	QueryCountKnown bool          `json:"queryCountKnown"`
	QueryDuration   time.Duration `json:"queryDuration,omitempty"`
	QueryPlanKnown  bool          `json:"queryPlanKnown"`
	QueryPlans      []QueryPlan   `json:"queryPlans,omitempty"`
	Reason          string        `json:"reason,omitempty"`
}

type QueryPlan struct {
	Statement string   `json:"statement"`
	Available bool     `json:"available"`
	Details   []string `json:"details,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

type queryRecorder struct {
	sync.Mutex
	count    int64
	observed bool
	duration time.Duration
	samples  []string
}

var queryRecorders sync.Map // map[*sql.DB]*queryRecorder

func traceSQLiteConnection(recorder *queryRecorder, connection *sqlite3.Conn) error {
	return connection.Trace(^sqlite3.TraceEvent(0), func(event sqlite3.TraceEvent, _, value any) error {
		recorder.Lock()
		defer recorder.Unlock()
		if event&sqlite3.TRACE_STMT != 0 {
			recorder.observed = true
			recorder.count++
			if statement, ok := value.(string); ok && readOnlyStatement(statement) && len(recorder.samples) < 32 {
				seen := false
				for _, sample := range recorder.samples {
					if sample == statement {
						seen = true
						break
					}
				}
				if !seen {
					recorder.samples = append(recorder.samples, statement)
				}
			}
		}
		if event&sqlite3.TRACE_PROFILE != 0 {
			recorder.observed = true
			if duration, ok := value.(int64); ok {
				recorder.duration += time.Duration(duration)
			}
		}
		return nil
	})
}

// OpenSQLite creates a traced SQLite pool using the driver's connection
// initializer, ensuring every physical connection is instrumented.
func OpenSQLite(dataSourceName string) (*sql.DB, error) {
	recorder := &queryRecorder{}
	database, err := sqliteDriver.Open(dataSourceName, func(connection *sqlite3.Conn) error { return traceSQLiteConnection(recorder, connection) })
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	queryRecorders.Store(database, recorder)
	return database, nil
}

// AttachSQLiteTrace attaches the bundled SQLite driver's statement/profile
// trace to a database pool. The pool is constrained to one physical
// connection so the trace covers every query issued through that pool.
func AttachSQLiteTrace(database *sql.DB) error {
	if database == nil {
		return errors.New("database is required")
	}
	database.SetMaxOpenConns(1)
	recorder := &queryRecorder{}
	connection, err := database.Conn(context.Background())
	if err != nil {
		return err
	}
	err = connection.Raw(func(driverConn any) error {
		wrappedConnection, ok := driverConn.(interface{ Raw() *sqlite3.Conn })
		if !ok {
			return fmt.Errorf("unsupported SQLite driver connection %T", driverConn)
		}
		return traceSQLiteConnection(recorder, wrappedConnection.Raw())
	})
	_ = connection.Close()
	if err == nil {
		queryRecorders.Store(database, recorder)
	}
	return err
}

func InspectIO(ctx context.Context, root string) IOReport {
	started := time.Now()
	report := IOReport{Observed: true}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if path != root && (base == ".git" || base == ".rsch" || base == "node_modules" || base == "vendor" || base == "build" || base == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if report.Files >= 10000 {
			report.Truncated = true
			return nil
		}
		report.Files++
		report.Bytes += info.Size()
		return nil
	})
	report.Duration = time.Since(started)
	if err != nil {
		report.Error = err.Error()
	}
	return report
}

// InspectBundle measures common local build-artifact directories without
// running a build. It reports unavailable when the project has no recognized
// artifact directory, rather than treating a missing bundle as zero bytes.
func InspectBundle(ctx context.Context, root string) BundleReport {
	candidates := []string{"dist", "build", "out", "bundle"}
	for _, candidate := range candidates {
		path := filepath.Join(root, candidate)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		report := BundleReport{Observed: true, Path: path}
		err = filepath.Walk(path, func(current string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if report.Files >= 10000 {
				report.Truncated = true
				return nil
			}
			report.Files++
			report.Bytes += info.Size()
			return nil
		})
		if err != nil {
			report.Reason = err.Error()
		}
		return report
	}
	return BundleReport{Reason: "no dist, build, out, or bundle artifact directory was found"}
}

func EvaluateBudget(name string, budget, actual int64, observed bool, reason string) BudgetCheck {
	check := BudgetCheck{Name: name, Configured: budget > 0, Observed: observed, Budget: budget, Actual: actual, Reason: reason}
	if budget <= 0 {
		check.Configured = false
		return check
	}
	if !observed {
		return check
	}
	within := actual <= budget
	check.Within = &within
	return check
}

func BudgetChecksComplete(checks []BudgetCheck) bool {
	for _, check := range checks {
		if !check.Configured {
			continue
		}
		if !check.Observed || check.Within == nil || !*check.Within {
			return false
		}
	}
	return true
}

func InspectDatabase(database *sql.DB) DatabaseReport {
	if database == nil {
		return DatabaseReport{Reason: "database is not configured"}
	}
	stats := database.Stats()
	report := DatabaseReport{Available: true, OpenConnections: stats.OpenConnections, InUse: stats.InUse, Idle: stats.Idle, WaitCount: stats.WaitCount, WaitDuration: stats.WaitDuration, QueryCountKnown: false, Reason: "database/sql pool statistics available; query count requires query instrumentation"}
	if value, ok := queryRecorders.Load(database); ok {
		recorder := value.(*queryRecorder)
		recorder.Lock()
		report.QueryCount, report.QueryDuration, report.QueryCountKnown = recorder.count, recorder.duration, recorder.observed
		samples := append([]string(nil), recorder.samples...)
		if recorder.observed {
			report.Reason = "SQLite statement/profile trace observed"
		} else {
			report.Reason = "SQLite trace attached but no statements observed"
		}
		recorder.Unlock()
		report.QueryPlans = inspectQueryPlans(database, samples)
		for _, plan := range report.QueryPlans {
			if plan.Available {
				report.QueryPlanKnown = true
				break
			}
		}
	}
	return report
}

func readOnlyStatement(statement string) bool {
	trimmed := strings.TrimSpace(statement)
	if len(trimmed) >= 7 && strings.EqualFold(trimmed[:7], "EXPLAIN") {
		return false
	}
	for _, prefix := range []string{"SELECT", "WITH"} {
		if len(trimmed) >= len(prefix) && strings.EqualFold(trimmed[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

func inspectQueryPlans(database *sql.DB, samples []string) []QueryPlan {
	plans := make([]QueryPlan, 0, len(samples))
	for _, statement := range samples {
		plan := QueryPlan{Statement: statement, Details: []string{}}
		args := make([]any, strings.Count(statement, "?"))
		rows, err := database.Query("EXPLAIN QUERY PLAN "+statement, args...)
		if err != nil {
			plan.Reason = "query plan unavailable: " + err.Error()
			plans = append(plans, plan)
			continue
		}
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if scanErr := rows.Scan(&id, &parent, &notUsed, &detail); scanErr != nil {
				plan.Reason = "query plan row unavailable: " + scanErr.Error()
				break
			}
			plan.Details = append(plan.Details, detail)
		}
		if err := rows.Err(); err != nil && plan.Reason == "" {
			plan.Reason = "query plan iteration failed: " + err.Error()
		}
		_ = rows.Close()
		plan.Available = len(plan.Details) > 0 && plan.Reason == ""
		plans = append(plans, plan)
	}
	return plans
}

func LoadProjectBudget(root string) (ProjectBudget, error) {
	data, err := os.ReadFile(filepath.Join(root, ".rsch", "performance.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectBudget{}, nil
		}
		return ProjectBudget{}, err
	}
	var budget ProjectBudget
	if err := json.Unmarshal(data, &budget); err != nil {
		return ProjectBudget{}, fmt.Errorf("invalid .rsch/performance.json: %w", err)
	}
	if budget.BuildTimeMS < 0 || budget.StartupMS < 0 || budget.BundleBytes < 0 || budget.MemoryBytes < 0 || budget.QueryCount < 0 || budget.LatencyMS < 0 {
		return ProjectBudget{}, errors.New("performance budgets cannot be negative")
	}
	return budget, nil
}

func MeasureBuild(ctx context.Context, root string, budget int64) Measurement {
	started := time.Now()
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"go", "build", "./..."}, Directory: root})
	measurement := Measurement{Kind: "build", Duration: time.Since(started), Observed: true, Budget: budget}
	if err != nil {
		measurement.Error = err.Error()
		if len(result.Output) > 0 {
			measurement.Error += ": " + string(result.Output)
		}
	}
	if budget > 0 {
		within := measurement.Duration <= time.Duration(budget)*time.Millisecond
		measurement.Within = &within
	}
	return measurement
}

func MeasureStartup(ctx context.Context, executable string, budget int64) Measurement {
	started := time.Now()
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{executable, "--version"}})
	measurement := Measurement{Kind: "startup", Duration: time.Since(started), Observed: true, Budget: budget}
	if err != nil {
		measurement.Error = err.Error()
		if len(result.Output) > 0 {
			measurement.Error += ": " + string(result.Output)
		}
	}
	if budget > 0 {
		within := measurement.Duration <= time.Duration(budget)*time.Millisecond
		measurement.Within = &within
	}
	return measurement
}

type Usage struct {
	StartedAt time.Time     `json:"startedAt"`
	Elapsed   time.Duration `json:"elapsed"`
	Calls     int64         `json:"calls"`
	Bytes     int64         `json:"bytes"`
	Cost      float64       `json:"cost"`
}

type Tracker struct {
	mu       sync.Mutex
	budget   Budget
	started  time.Time
	usage    Usage
	notifier notification.Provider
}

func (t *Tracker) SetNotifier(provider notification.Provider) {
	if t != nil {
		t.mu.Lock()
		t.notifier = provider
		t.mu.Unlock()
	}
}

func New(budget Budget) *Tracker {
	now := time.Now().UTC()
	return &Tracker{budget: budget, started: now, usage: Usage{StartedAt: now}}
}

func (t *Tracker) Consume(calls, bytes int64, cost float64) error {
	if t == nil {
		return errors.New("performance tracker is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if calls < 0 || bytes < 0 || cost < 0 {
		return errors.New("usage increments cannot be negative")
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		return errors.New("usage cost must be finite")
	}
	if calls > math.MaxInt64-t.usage.Calls || bytes > math.MaxInt64-t.usage.Bytes || math.IsInf(t.usage.Cost+cost, 0) {
		return errors.New("usage increments exceed accounting capacity")
	}
	next := t.usage
	next.Calls += calls
	next.Bytes += bytes
	next.Cost += cost
	next.Elapsed = time.Since(t.started)
	if t.budget.MaxCalls > 0 && next.Calls > t.budget.MaxCalls || t.budget.MaxBytes > 0 && next.Bytes > t.budget.MaxBytes || t.budget.MaxCost > 0 && next.Cost > t.budget.MaxCost || t.budget.MaxDuration > 0 && next.Elapsed > t.budget.MaxDuration {
		if t.notifier != nil {
			_ = t.notifier.Notify(context.Background(), notification.Event{Kind: "budget.exceeded", Message: "performance budget exceeded", Metadata: map[string]string{"calls": fmt.Sprint(next.Calls), "bytes": fmt.Sprint(next.Bytes), "cost": fmt.Sprintf("%.6f", next.Cost)}})
		}
		return ErrBudgetExceeded
	}
	t.usage = next
	return nil
}

func (t *Tracker) Check() error { return t.Consume(0, 0, 0) }
func (t *Tracker) Snapshot() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.usage
	out.Elapsed = time.Since(t.started)
	return out
}
func (t *Tracker) Budget() Budget {
	if t == nil {
		return Budget{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget
}

func Measure[T any](tracker *Tracker, fn func() (T, int64, float64)) (T, error) {
	var zero T
	if tracker == nil {
		return zero, errors.New("performance tracker is required")
	}
	value, bytes, cost := fn()
	if err := tracker.Consume(1, bytes, cost); err != nil {
		return zero, err
	}
	return value, nil
}
