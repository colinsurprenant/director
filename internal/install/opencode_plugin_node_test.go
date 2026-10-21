package install

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOpenCodePluginNodeSmoke runs the dependency-free offline smoke test
// (opencode/director.smoke.mjs) against the plugin exactly as install writes it,
// placeholder substituted. Node is the plugin's own runtime family, so this is
// the one place the shape and the never-reject guarantees are executed rather
// than read; it skips where node is absent (the CI runners ship one, a
// contributor's machine may not). It proves nothing about OpenCode itself
// accepting the shape: that is the live canary (hack/canary/opencode).
func TestOpenCodePluginNodeSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	// The smoke test uses AbortSignal, for-await and Atomics.wait on the main
	// thread: all long-standing, but an ancient node would fail for reasons that
	// say nothing about the plugin.
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skipf("node --version failed: %v", err)
	}
	if major, convErr := strconv.Atoi(strings.SplitN(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), ".", 2)[0]); convErr != nil || major < 18 {
		t.Skipf("node %s is older than 18", strings.TrimSpace(string(out)))
	}

	// A .mjs copy so node reads it as a module without a package.json beside it;
	// the content is what OpenCode loads as director.js.
	plugin := filepath.Join(t.TempDir(), "director.mjs")
	if err := writeOpenCodePlugin(plugin, filepath.Join(t.TempDir(), "hooks")); err != nil {
		t.Fatalf("writeOpenCodePlugin: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join("opencode", "director.smoke.mjs"), plugin)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	scratch := t.TempDir()
	// A developer's own NODE_OPTIONS must not leak into the fake binary, and the
	// script's scratch dir lands somewhere the test cleans up.
	cmd.Env = append(os.Environ(), "NODE_OPTIONS=", "TMPDIR="+scratch, "TEMP="+scratch, "TMP="+scratch)
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		t.Fatalf("plugin smoke test failed: %v\n%s", err, combined.String())
	}
	t.Logf("\n%s", combined.String())
}
