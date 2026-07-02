package main

import "testing"

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
