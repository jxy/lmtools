// Package version reports the identity the go command stamps into a binary:
// the main module version, the VCS revision it was built from, and the Go
// toolchain that built it.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// String renders the line a -version flag prints, for example
//
//	lmc version v0.1.0 built with go1.26.8 linux/amd64 from da4e1e6a9188 on 2026-09-21T04:33:00Z
//
// The module version is what the go command found at build time: the tag on
// the built commit, a pseudo-version when the commit carries no tag, or
// "(devel)" when the build ran outside a checkout or with a go command older
// than 1.24. A "+dirty" suffix on the version, or "(modified)" after the
// revision, marks uncommitted changes in the checkout.
func String(name string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return name + " version unknown"
	}
	return render(name, info, runtime.GOOS+"/"+runtime.GOARCH)
}

func render(name string, info *debug.BuildInfo, platform string) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(" version ")
	moduleVersion := info.Main.Version
	if moduleVersion == "" {
		moduleVersion = "unknown"
	}
	b.WriteString(moduleVersion)
	if info.GoVersion != "" {
		b.WriteString(" built with ")
		b.WriteString(info.GoVersion)
		b.WriteString(" ")
		b.WriteString(platform)
	}

	var revision, at string
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		b.WriteString(" from ")
		b.WriteString(revision)
		if modified && !strings.HasSuffix(moduleVersion, "+dirty") {
			b.WriteString(" (modified)")
		}
	}
	if at != "" {
		b.WriteString(" on ")
		b.WriteString(at)
	}
	return b.String()
}
