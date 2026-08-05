package main

import "runtime/debug"

// sourceVersion is the release represented by the checked-in source tree. Go
// stamps a real module version for `go install ...@version` and GoReleaser
// builds, but local `go run` / `go install ./cmd/engram` builds report
// "(devel)". Keep this fallback in step with each patch release so every
// installation path can report a useful version.
const sourceVersion = "v0.13.0"

// engramVersion returns the module version stamped into the binary at build
// time (e.g. "v0.12.3" from `go install ...@v0.12.3`), falling back to the
// checked-in source version for local builds. It is the single source of truth
// for --version, save archives, shipped guidance, and inject.
func engramVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return resolveEngramVersion(info.Main.Version)
	}
	return sourceVersion
}

func resolveEngramVersion(buildVersion string) string {
	if buildVersion != "" && buildVersion != "(devel)" {
		return buildVersion
	}
	return sourceVersion
}
