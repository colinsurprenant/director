package adopt

// scan.go is the Tier-1 open-loop discovery: a bounded, grep-style pass over a
// repo's *tracked* files for the markers that signal an open loop a director
// would otherwise carry only in their head — TODO/FIXME/deferred and unchecked
// checklist items (§6 step 1, §17). It deliberately does NO code-mapping and NO
// doc living/record/rot classification; that heavier reconciliation is Tier 2,
// deferred to a fast-follow. The relevance bar is dead-simple in v1 (§12): this
// surfaces *all* candidates and the human picks which to import — the §12
// auto-seed heuristic is a later refinement.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Scan bounds. A repo can be huge; an unbounded grep would blow up memory and
// bury the meaningful loops. These caps keep the scan cheap and, crucially, when
// any cap trips the result is flagged Truncated (never a silent cut — §9).
const (
	maxFiles      = 5000      // tracked files inspected before we stop walking
	maxFileBytes  = 1 << 20   // 1 MiB: skip larger files (vendored blobs, data)
	maxLineBytes  = 64 * 1024 // longest line we scan; longer lines are skipped
	maxCandidates = 2000      // candidates returned before we stop collecting
	maxBodyRunes  = 280       // candidate text is trimmed to keep open-items terse
)

// markers are the open-loop signals scanned for, case-insensitively. They are the
// v1 set from §6/§17: explicit deferral words plus the unchecked-checklist token.
// Checklist items are matched structurally (a leading "- [ ]") rather than by this
// list, since "[ ]" is positional; see scanLine.
var markers = []string{"TODO", "FIXME", "DEFERRED", "HACK", "XXX"}

// Candidate is one open-loop hit: the tracked file it came from (repo-relative as
// git reports it), the 1-based line number, and the surrounding line's text. The
// text becomes the imported open-item's body, so it carries enough context to be
// actionable on its own.
type Candidate struct {
	File string // repo-relative path, as `git ls-files` reports it
	Line int    // 1-based line number within File
	Text string // the matched line, trimmed (the open-item body)
}

// ScanOpenLoops returns the open-loop candidates found in the tracked files of the
// repo at dir. It restricts to tracked files via `git ls-files` (so build output,
// vendored trees, and everything under .git are excluded by construction) and
// bounds the work by file count, file size, and candidate count.
//
// truncated reports whether any bound was hit — i.e. whether the returned slice is
// a complete picture (false) or a capped sample (true). Callers MUST surface a
// truncation rather than treat a capped result as exhaustive (§9: no silent caps).
func ScanOpenLoops(dir string) (candidates []Candidate, truncated bool, err error) {
	// Resolve the repository toplevel first: `git ls-files` lists only the files
	// beneath its working directory, so running the scan from a nested subdirectory
	// would silently miss open loops elsewhere in the same repo (a §9 silent-cap
	// violation). Listing and scanning from the root makes the import complete
	// regardless of where adopt was invoked.
	root, err := repoRoot(dir)
	if err != nil {
		return nil, false, err
	}

	files, err := trackedFiles(root)
	if err != nil {
		return nil, false, err
	}

	if len(files) > maxFiles {
		files = files[:maxFiles]
		truncated = true
	}

	for _, rel := range files {
		hits, fileTruncated, err := scanFile(root, rel)
		if err != nil {
			return nil, false, err
		}
		if fileTruncated {
			truncated = true
		}
		for _, h := range hits {
			if len(candidates) >= maxCandidates {
				return candidates, true, nil
			}
			candidates = append(candidates, h)
		}
	}
	return candidates, truncated, nil
}

// repoRoot resolves the repository toplevel for dir via git, so the scan always
// covers the whole repo rather than just the subtree under the invocation cwd.
func repoRoot(dir string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("adopt: resolve repo root from %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// trackedFiles lists the repo's tracked files relative to its root via git, the
// same injectable boundary the identity package crosses. -z null-delimits so paths
// with spaces or newlines survive intact. A repo with no tracked files yields an
// empty list, not an error.
func trackedFiles(dir string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("adopt: git ls-files in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}

	raw := stdout.Bytes()
	var files []string
	for _, p := range bytes.Split(raw, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	return files, nil
}

// scanFile reads one tracked file (if it still exists on disk and is within the
// size cap) and returns its open-loop hits. A file that vanished between ls-files
// and the read, a symlink/special file, or one over the size cap is skipped —
// skipping an over-cap file is reported via truncated so the caller knows the
// picture is partial.
func scanFile(dir, rel string) (hits []Candidate, truncated bool, err error) {
	full := filepath.Join(dir, filepath.FromSlash(rel))

	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		// A symlink, special file, or one removed since ls-files — skip quietly;
		// it is not a readable source file with open loops.
		return nil, false, nil
	}
	if info.Size() > maxFileBytes {
		return nil, true, nil // too big to scan: skipped, picture is partial.
	}

	f, err := os.Open(full)
	if err != nil {
		return nil, false, nil // unreadable now — skip rather than fail the whole scan.
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if text, ok := scanLine(sc.Text()); ok {
			hits = append(hits, Candidate{File: rel, Line: lineNo, Text: text})
		}
	}
	if scErr := sc.Err(); scErr != nil {
		// A line longer than maxLineBytes (or a read error) ends this file's scan;
		// flag truncation so an over-long line isn't silently treated as clean.
		return hits, true, nil
	}
	return hits, false, nil
}

// scanLine decides whether a single line is an open-loop candidate and, if so,
// returns the trimmed body. A line qualifies when it is an unchecked checklist
// item (`- [ ]`, the markdown TODO) or contains one of the marker words
// case-insensitively. The returned text is whitespace-trimmed and length-capped so
// imported open-items stay terse.
func scanLine(line string) (string, bool) {
	if isUncheckedChecklist(line) {
		return trimBody(line), true
	}
	upper := strings.ToUpper(line)
	for _, m := range markers {
		if strings.Contains(upper, m) {
			return trimBody(line), true
		}
	}
	return "", false
}

// isUncheckedChecklist matches a markdown unchecked task item — optional leading
// whitespace, a list bullet (-, *, +), then "[ ]". A checked "[x]" is a CLOSED
// loop and deliberately does not match.
func isUncheckedChecklist(line string) bool {
	s := strings.TrimLeft(line, " \t")
	if len(s) < 4 {
		return false
	}
	bullet := s[0]
	if bullet != '-' && bullet != '*' && bullet != '+' {
		return false
	}
	rest := strings.TrimLeft(s[1:], " \t")
	return strings.HasPrefix(rest, "[ ]")
}

// trimBody normalizes a matched line into an open-item body: outer whitespace
// stripped and the text capped at maxBodyRunes (rune-safe) so a pathological line
// can't bloat the LOG. An ellipsis marks a truncation.
func trimBody(line string) string {
	s := strings.TrimSpace(line)
	r := []rune(s)
	if len(r) > maxBodyRunes {
		return string(r[:maxBodyRunes]) + "…"
	}
	return s
}
