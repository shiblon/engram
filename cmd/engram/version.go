package main

import (
	"runtime/debug"
	"strings"
)

// sourceVersion is the release represented by the checked-in source tree. Go
// stamps a real module version for `go install ...@version` and GoReleaser
// builds, but local `go run` / `go install ./cmd/engram` builds report
// "(devel)". Keep this fallback in step with each patch release so every
// installation path can report a useful version.
const sourceVersion = "v0.13.2"

// engramVersion returns the module version stamped into the binary at build
// time (e.g. "v0.12.3" from `go install ...@v0.12.3`), falling back to the
// checked-in source version plus local build detail. It is the single source of
// truth for --version, save archives, shipped guidance, and inject.
func engramVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return resolveEngramVersion("", "", false)
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return resolveEngramVersion(info.Main.Version, revision, modified)
}

// resolveEngramVersion decides what a binary should call itself.
//
// A local build must never claim to BE the release named by sourceVersion. It
// previously did: "(devel)" fell straight back to the bare release string, so a
// tree twelve commits ahead of v0.13.1 reported exactly "v0.13.1". That misleads on
// its own, and it also defeats the drift check this repo exists to run -- inject
// compares the running version against the version stamped into the shipped
// guidance, and two strings laundered into the same release number compare equal
// and stay silent.
//
// So a local build carries build metadata: the short revision, marked dirty when
// the tree has uncommitted changes. Where no revision is available it says so
// rather than guessing. Worth knowing why that case is common here -- Go does NOT
// stamp vcs.* when building from a LINKED GIT WORKTREE. Same repo, same command,
// no revision, which is exactly what produced a bare "v0.13.1" from a tree full of
// unreleased work.
//
// The "+..." form is semver build metadata, ignored for version precedence, so
// nothing comparing releases is disturbed by it.
func resolveEngramVersion(buildVersion, revision string, modified bool) string {
	if !isLocalBuildVersion(buildVersion) {
		return buildVersion
	}
	// Go marks a dirty tree in the stamped version itself; believe either signal.
	if strings.HasSuffix(buildVersion, "+dirty") {
		modified = true
	}
	if revision == "" {
		// Fall back to any revision Go embedded in a pseudo-version, since that is
		// better than admitting nothing.
		revision = revisionFromPseudoVersion(buildVersion)
	}
	if revision == "" {
		// No VCS stamp: a linked worktree, or -buildvcs=false. Say "devel" rather
		// than impersonating the release.
		return sourceVersion + "+devel"
	}
	suffix := shortRevision(revision)
	if modified {
		suffix += ".dirty"
	}
	return sourceVersion + "+" + suffix
}

// isLocalBuildVersion reports whether a stamped module version means "built from a
// working tree" rather than "installed as a release".
//
// Two spellings, and the second is why this stopped working. Older toolchains said
// "(devel)". Current ones stamp a PSEUDO-VERSION instead -- v0.0.0-<timestamp>-<hash>,
// optionally +dirty -- which is honest but is not "(devel)", so a check looking only
// for that string passed it straight through as if it were a release. It is also
// less useful to a human than a release plus a commit, because it hides which
// release the tree is based on.
func isLocalBuildVersion(version string) bool {
	switch {
	case version == "", version == "(devel)":
		return true
	case strings.HasPrefix(version, "v0.0.0-"):
		return true // pseudo-version: no tagged release describes this build
	}
	return false
}

// revisionFromPseudoVersion pulls the commit hash out of v0.0.0-<timestamp>-<hash>.
func revisionFromPseudoVersion(version string) string {
	trimmed := strings.TrimSuffix(version, "+dirty")
	if index := strings.LastIndexByte(trimmed, '-'); index >= 0 {
		return trimmed[index+1:]
	}
	return ""
}

// shortRevision abbreviates a git revision the way git does by default.
func shortRevision(revision string) string {
	const shortLen = 7
	if len(revision) <= shortLen {
		return revision
	}
	return revision[:shortLen]
}

// releaseVersion strips build metadata, yielding the release a version belongs to.
// Callers comparing releases rather than exact builds want this.
func releaseVersion(version string) string {
	if index := strings.IndexByte(version, '+'); index >= 0 {
		return version[:index]
	}
	return version
}
