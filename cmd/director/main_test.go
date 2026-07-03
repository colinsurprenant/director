package main

import (
	"runtime/debug"
	"testing"
)

// TestDispatchExitCodes locks the dispatch contract, including the load-bearing
// fail-safe invariant that the hook path never returns non-zero (§5.4, §13 t5)
// even before its real handlers exist.
func TestDispatchExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no args shows usage, non-zero", nil, 2},
		{"help is zero", []string{"help"}, 0},
		{"version is zero", []string{"version"}, 0},
		{"--version is zero", []string{"--version"}, 0},
		{"version rejects extra args", []string{"version", "foo"}, 2},
		{"unknown verb is non-zero", []string{"bogus"}, 2},
		{"hook without event is fail-safe", []string{"_hook"}, 0},
		{"hook with event is fail-safe", []string{"_hook", "sessionstart"}, 0},
		// M1: a --project path-traversal value is rejected as user error (exit 2),
		// never reaching the store/manifest path-build.
		{"render rejects traversal project", []string{"render", "--project", "../../tmp/evil"}, 2},
		{"render rejects dotdot project", []string{"render", "--project", ".."}, 2},
		{"brief rejects traversal project", []string{"brief", "--project", "../../tmp/evil"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args); got != tt.want {
				t.Fatalf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

// TestVersionLine locks the version output contract the release workflow's
// -X main.version stamping relies on.
func TestVersionLine(t *testing.T) {
	if got, want := versionLine(), "director dev"; got != want {
		t.Fatalf("versionLine() = %q, want %q (source build)", got, want)
	}
	orig := version
	defer func() { version = orig }()
	version = "v1.2.3"
	if got, want := versionLine(), "director v1.2.3"; got != want {
		t.Fatalf("versionLine() = %q, want %q (stamped build)", got, want)
	}
}

// TestResolveVersion locks the fallback chain: release stamp wins outright; an
// unstamped binary reports the module version Go embeds on `go install` (the
// bug this fixes: go-install'd v1.3.0 printed "dev"); a true source build —
// "(devel)", empty, or no build info at all — stays "dev".
func TestResolveVersion(t *testing.T) {
	withMain := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	cases := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"stamp wins over module version", "v1.2.3", withMain("v9.9.9"), "v1.2.3"},
		{"go-install module version", "dev", withMain("v1.3.0"), "v1.3.0"},
		{"source build (devel)", "dev", withMain("(devel)"), "dev"},
		{"empty module version", "dev", withMain(""), "dev"},
		{"no build info", "dev", noInfo, "dev"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.stamped, tt.read); got != tt.want {
				t.Fatalf("resolveVersion(%q) = %q, want %q", tt.stamped, got, tt.want)
			}
		})
	}
}
