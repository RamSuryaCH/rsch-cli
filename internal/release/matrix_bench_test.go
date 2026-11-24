package release

import "testing"

func BenchmarkSupportedTargets(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = SupportedTargets()
	}
}

func BenchmarkValidateTarget(b *testing.B) {
	target := Target{GOOS: "linux", GOARCH: "amd64"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ValidateTarget(target); err != nil {
			b.Fatal(err)
		}
	}
}
