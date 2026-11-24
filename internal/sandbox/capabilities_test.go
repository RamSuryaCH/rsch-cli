package sandbox

import "testing"

func TestDetectReportsPlatform(t *testing.T) {
	capability := Detect()
	if capability.Platform == "" {
		t.Fatal("missing platform")
	}
	if capability.State == "" {
		t.Fatal("missing capability state")
	}
	if !capability.Isolated && capability.Reason == "" {
		t.Fatal("unexplained unavailable capability")
	}
}
