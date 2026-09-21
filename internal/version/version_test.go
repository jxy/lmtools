package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func vcs(revision, at string, modified bool) []debug.BuildSetting {
	dirty := "false"
	if modified {
		dirty = "true"
	}
	return []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: at},
		{Key: "vcs.modified", Value: dirty},
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "release tag from a clean checkout",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.8",
				Main:      debug.Module{Version: "v0.1.0"},
				Settings:  vcs("da4e1e6a91889b62f0ac5caef9072822d42eb40", "2026-09-21T04:33:00Z", false),
			},
			want: "lmc version v0.1.0 built with go1.26.8 linux/amd64 from da4e1e6a9188 on 2026-09-21T04:33:00Z",
		},
		{
			name: "dirty pseudo-version carries its own marker",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.8",
				Main:      debug.Module{Version: "v0.0.0-20260921045247-9550285dfbde+dirty"},
				Settings:  vcs("9550285dfbde9c96ee1566f00ed760ffab220a19", "2026-09-21T04:52:47Z", true),
			},
			want: "lmc version v0.0.0-20260921045247-9550285dfbde+dirty built with go1.26.8 linux/amd64 from 9550285dfbde on 2026-09-21T04:52:47Z",
		},
		{
			name: "old go command records no version so the revision carries the marker",
			info: &debug.BuildInfo{
				GoVersion: "go1.21.13",
				Main:      debug.Module{Version: "(devel)"},
				Settings:  vcs("f34133a6a91889b62f0ac5caef9072822d42eb40", "2026-09-21T03:46:04Z", true),
			},
			want: "lmc version (devel) built with go1.21.13 linux/amd64 from f34133a6a918 (modified) on 2026-09-21T03:46:04Z",
		},
		{
			name: "no vcs information",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.8",
				Main:      debug.Module{Version: "v0.1.0"},
			},
			want: "lmc version v0.1.0 built with go1.26.8 linux/amd64",
		},
		{
			name: "no module version",
			info: &debug.BuildInfo{GoVersion: "go1.26.8"},
			want: "lmc version unknown built with go1.26.8 linux/amd64",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render("lmc", tt.info, "linux/amd64"); got != tt.want {
				t.Fatalf("render() =\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}

// TestStringReadsTheRunningBinary checks the real build info path: the test
// binary always has one, so the line names the program and the toolchain.
func TestStringReadsTheRunningBinary(t *testing.T) {
	got := String("lmc")
	if !strings.HasPrefix(got, "lmc version ") || !strings.Contains(got, " built with go") {
		t.Fatalf("String() = %q, want a line starting with %q and naming the toolchain", got, "lmc version ")
	}
}
