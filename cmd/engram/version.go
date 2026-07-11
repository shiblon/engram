package main

import "runtime/debug"

// engramVersion returns the module version stamped into the binary at build
// time (e.g. "v0.7.0" from `go install ...@v0.7.0`), or "(devel)" for a local
// build with no version info. Single source of truth for the version string
// shown by --version, embedded in save archives, stamped into the shipped
// guidance, and reported by inject.
func engramVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
