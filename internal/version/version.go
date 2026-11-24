package version

import "runtime/debug"

// Build-time parameters set via -ldflags. Source builds use a stable, useful
// development identifier instead of reporting an unknown version.
var Version = "dev"

// RSCH is distributed as the `rsch` executable. When installed from a module,
// use the embedded module version unless a release build injected one.
func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// < go v1.18
		return
	}
	mainVersion := info.Main.Version
	if mainVersion == "" || mainVersion == "(devel)" {
		// bin not built using `go install`
		return
	}
	// bin built using `go install`
	Version = mainVersion
}
