package identity

import (
	"os"
	"path/filepath"
	"strings"
)

const repoKeyFile = ".director/repo-key"

// RepoKey returns the canonical, collision-free key for the repo containing dir.
// It is derive-once: once persisted at <toplevel>/.director/repo-key the stored
// value is returned verbatim, so a later remote change cannot shift it (§4.3,
// §15.6). On first run it derives via the fallback chain and persists the result.
func RepoKey(dir string) (string, error) {
	return repoKey(dir, runGit)
}

func repoKey(dir string, git gitRunner) (string, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return repoKeyAt(top, dir, git)
}

// repoKeyAt is repoKey with the repo toplevel already resolved by the caller. It
// exists so resolve() — which needs the toplevel for its own persistence path —
// can derive `git rev-parse --show-toplevel` once and reuse it for both, sparing
// an extra git fork on the hot hook path (identity.Resolve runs per PostToolUse).
func repoKeyAt(top, dir string, git gitRunner) (string, error) {
	keyPath := filepath.Join(top, repoKeyFile)
	if b, err := os.ReadFile(keyPath); err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k, nil
		}
	}

	key, err := deriveRepoKey(dir, git)
	if err != nil {
		return "", err
	}
	if err := persist(keyPath, key); err != nil {
		return "", err
	}
	return key, nil
}

// deriveRepoKey runs the §4.3 fallback chain: a normalized remote URL when one
// exists (collapsing fork→upstream), else the slug of the shared git-common-dir
// — which collapses every worktree of one repo to a single key because they all
// share that directory.
func deriveRepoKey(dir string, git gitRunner) (string, error) {
	if url, ok := preferredRemote(dir, git); ok {
		return slugSegment(normalizeRemoteURL(url)), nil
	}

	commonDir, err := git(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(commonDir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		commonDir = filepath.Join(abs, commonDir)
	}
	if resolved, err := filepath.EvalSymlinks(commonDir); err == nil {
		commonDir = resolved
	}
	return "path-" + slugSegment(commonDir), nil
}

// preferredRemote returns the URL of the remote that best identifies the repo:
// upstream (so a fork collapses to its canonical parent) over origin over the
// first remote git lists. ok is false when the repo has no remotes.
func preferredRemote(dir string, git gitRunner) (string, bool) {
	out, err := git(dir, "remote")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", false
	}
	remotes := strings.Fields(out)
	for _, pref := range []string{"upstream", "origin"} {
		for _, r := range remotes {
			if r == pref {
				if url, err := git(dir, "remote", "get-url", r); err == nil && url != "" {
					return url, true
				}
			}
		}
	}
	if url, err := git(dir, "remote", "get-url", remotes[0]); err == nil && url != "" {
		return url, true
	}
	return "", false
}

// normalizeRemoteURL reduces any git remote URL form — https, ssh, scp-like,
// with or without credentials/port/.git suffix — to a stable host/path key so
// the same repo resolves identically however it was cloned. The host is
// lowercased; path case is preserved (path case can be significant off GitHub).
func normalizeRemoteURL(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.Contains(s, "://") {
		s = s[strings.Index(s, "://")+3:]
		if i := strings.Index(s, "@"); i >= 0 {
			s = s[i+1:]
		}
		if slash := strings.Index(s, "/"); slash >= 0 {
			hostport, rest := s[:slash], s[slash:]
			if c := strings.Index(hostport, ":"); c >= 0 {
				hostport = hostport[:c] // drop :port
			}
			s = hostport + rest
		}
	} else {
		if i := strings.Index(s, "@"); i >= 0 {
			s = s[i+1:] // drop user@ in scp-like form
		}
		s = strings.Replace(s, ":", "/", 1) // host:path -> host/path
	}
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git"), "/")
	if slash := strings.Index(s, "/"); slash >= 0 {
		return strings.ToLower(s[:slash]) + s[slash:]
	}
	return strings.ToLower(s)
}

// slugSegment collapses s into one filesystem-safe path segment: it keeps
// [A-Za-z0-9._-] and replaces every other run with a single '-'.
func slugSegment(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prevDash = false
		case r == '-':
			b.WriteRune(r)
			prevDash = true
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func persist(path, val string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(val+"\n"), 0o644)
}
