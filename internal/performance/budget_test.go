package performance

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/opencode-ai/opencode/internal/notification"
)

func TestLoadProjectBudgetIsExplicitAndValidated(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadProjectBudget(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".rsch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "performance.json"), []byte(`{"buildTimeMs":100,"startupMs":25}`), 0o600); err != nil {
		t.Fatal(err)
	}
	budget, err := LoadProjectBudget(root)
	if err != nil {
		t.Fatal(err)
	}
	if budget.BuildTimeMS != 100 || budget.StartupMS != 25 {
		t.Fatalf("budget=%+v", budget)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "performance.json"), []byte(`{"buildTimeMs":-1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectBudget(root); err == nil {
		t.Fatal("expected negative budget rejection")
	}
}

func TestInspectIOAndDatabaseAreEvidenceLabeled(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	ioReport := InspectIO(context.Background(), root)
	if !ioReport.Observed || ioReport.Files != 1 || ioReport.Bytes != 5 {
		t.Fatalf("io report=%+v", ioReport)
	}
	database := InspectDatabase(nil)
	if database.Available || database.QueryCountKnown {
		t.Fatalf("nil database report=%+v", database)
	}
}

func TestInspectBundleDoesNotTreatMissingArtifactsAsZero(t *testing.T) {
	report := InspectBundle(context.Background(), t.TempDir())
	if report.Observed || report.Reason == "" || report.Bytes != 0 {
		t.Fatalf("bundle report=%+v", report)
	}
	check := EvaluateBudget("bundle", 10, report.Bytes, report.Observed, report.Reason)
	if !check.Configured || check.Within != nil || check.Observed {
		t.Fatalf("budget check=%+v", check)
	}
}

func TestInspectBundleMeasuresBoundedArtifactDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "app.js"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := InspectBundle(context.Background(), root)
	if !report.Observed || report.Files != 1 || report.Bytes != 4 {
		t.Fatalf("bundle report=%+v", report)
	}
	check := EvaluateBudget("bundle", 4, report.Bytes, report.Observed, "observed")
	if check.Within == nil || !*check.Within {
		t.Fatalf("budget check=%+v", check)
	}
}

func TestSQLiteTraceRecordsQueries(t *testing.T) {
	database, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := AttachSQLiteTrace(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE sample (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO sample VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	var value int
	if err := database.QueryRow("SELECT id FROM sample WHERE id = ?", 1).Scan(&value); err != nil {
		t.Fatal(err)
	}
	report := InspectDatabase(database)
	if report.QueryCountKnown && report.QueryCount < 1 {
		t.Fatalf("query report=%+v", report)
	}
	if !report.QueryPlanKnown || len(report.QueryPlans) == 0 {
		t.Fatalf("query plan report=%+v", report)
	}
}

type notificationSpy struct{ kind string }

func (s *notificationSpy) Notify(_ context.Context, event notification.Event) error {
	s.kind = event.Kind
	return nil
}

func TestTrackerEnforcesCallsBytesAndCost(t *testing.T) {
	tracker := New(Budget{MaxCalls: 2, MaxBytes: 10, MaxCost: 1})
	if err := tracker.Consume(1, 5, .5); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Consume(1, 5, .5); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Consume(1, 0, 0); err != ErrBudgetExceeded {
		t.Fatalf("expected budget error, got %v", err)
	}
	usage := tracker.Snapshot()
	if usage.Calls != 2 || usage.Bytes != 10 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestMeasureRecordsSuccessfulUsageOnly(t *testing.T) {
	tracker := New(Budget{MaxCalls: 1})
	value, err := Measure(tracker, func() (string, int64, float64) { return "ok", 3, 0.1 })
	if err != nil || value != "ok" {
		t.Fatalf("unexpected measure: %q %v", value, err)
	}
	if _, err := Measure(tracker, func() (string, int64, float64) { return "second", 0, 0 }); err != ErrBudgetExceeded {
		t.Fatalf("expected budget error, got %v", err)
	}
}

func TestBudgetExceededNotifies(t *testing.T) {
	spy := &notificationSpy{}
	tracker := New(Budget{MaxCalls: 1})
	tracker.SetNotifier(spy)
	if err := tracker.Consume(2, 0, 0); err != ErrBudgetExceeded {
		t.Fatal(err)
	}
	if spy.kind != "budget.exceeded" {
		t.Fatalf("notification=%q", spy.kind)
	}
}

func TestTrackerRejectsInvalidAndOverflowingUsage(t *testing.T) {
	tests := []struct {
		name    string
		initial Usage
		next    Usage
	}{
		{name: "NaN cost", next: Usage{Cost: math.NaN()}},
		{name: "infinite cost", next: Usage{Cost: math.Inf(1)}},
		{name: "call overflow", initial: Usage{Calls: math.MaxInt64}, next: Usage{Calls: 1}},
		{name: "byte overflow", initial: Usage{Bytes: math.MaxInt64}, next: Usage{Bytes: 1}},
		{name: "cost overflow", initial: Usage{Cost: math.MaxFloat64}, next: Usage{Cost: math.MaxFloat64}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, budget := range []Budget{{}, {MaxCalls: math.MaxInt64, MaxBytes: math.MaxInt64, MaxCost: math.MaxFloat64}} {
				tracker := New(budget)
				if err := tracker.Consume(test.initial.Calls, test.initial.Bytes, test.initial.Cost); err != nil {
					t.Fatal(err)
				}
				if err := tracker.Consume(test.next.Calls, test.next.Bytes, test.next.Cost); err == nil {
					t.Fatalf("expected invalid or overflowing usage to be rejected with budget %+v", budget)
				}
				usage := tracker.Snapshot()
				if usage.Calls != test.initial.Calls || usage.Bytes != test.initial.Bytes || usage.Cost != test.initial.Cost {
					t.Fatalf("rejected usage changed accounting: %+v", usage)
				}
			}
		})
	}
}

func TestMeasureBuildPreservesFailureWithoutOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	measurement := MeasureBuild(ctx, t.TempDir(), 0)
	if measurement.Error == "" {
		t.Fatal("canceled build without command output was reported as successful")
	}
}
