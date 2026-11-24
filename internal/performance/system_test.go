package performance

import "testing"

func TestInspectSystemReportsPortableAndUnavailableDimensions(t *testing.T) {
	report := InspectSystem(t.TempDir())
	if !report.CPU.Observed || report.CPU.Logical < 1 || !report.Memory.Observed {
		t.Fatalf("expected CPU and memory observations: %+v", report)
	}
	if report.Network.Observed || report.Network.Reason == "" {
		t.Fatalf("network telemetry must be explicit when unavailable: %+v", report.Network)
	}
}
