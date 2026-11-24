package risk

import "testing"

func TestAssessEscalatesDestructiveNetworkSecurityAction(t *testing.T) {
	assessment := Assess(Input{Tool: "browser", Path: "production/auth", Network: true, Mutating: true})
	if assessment.Level != Critical || len(assessment.Reasons) < 3 {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
}
