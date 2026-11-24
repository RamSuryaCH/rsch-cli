package release

import "testing"

func FuzzValidateTarget(f *testing.F) {
	for _, target := range SupportedTargets() {
		f.Add(target.GOOS, target.GOARCH)
	}
	f.Add("", "")
	f.Add("linux", "not-an-architecture")

	f.Fuzz(func(t *testing.T, goos, goarch string) {
		err := ValidateTarget(Target{GOOS: goos, GOARCH: goarch})
		if err == nil {
			for _, supported := range SupportedTargets() {
				if supported.GOOS == goos && supported.GOARCH == goarch {
					return
				}
			}
			t.Fatalf("accepted unsupported target %q/%q", goos, goarch)
		}
	})
}
