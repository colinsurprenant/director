package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/colinsurprenant/director/internal/event"
	"github.com/colinsurprenant/director/internal/render"
)

// TestSpecLifecycleTableMatchesTheVocabulary keeps the spec's table honest: it
// lists every value the code emits, per kind, and nothing else.
func TestSpecLifecycleTableMatchesTheVocabulary(t *testing.T) {
	spec, err := os.ReadFile("../../docs/specs/2026-09-14-json-projection-design.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[event.Kind]string{
		event.KindDecision: "| Decision |", event.KindOpenItem: "| Open Item |",
		event.KindHandoff: "| Handoff |", event.KindNote: "| Note |",
	}
	value := regexp.MustCompile("`([a-z-]+)`")
	for kind, prefix := range rows {
		var line string
		for _, l := range strings.Split(string(spec), "\n") {
			if strings.HasPrefix(l, prefix) {
				line = l
			}
		}
		if line == "" {
			t.Errorf("spec has no lifecycle table row for %s", kind)
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			t.Errorf("spec row for %s is malformed: %q", kind, line)
			continue
		}
		var got []string
		for _, m := range value.FindAllStringSubmatch(cells[2], -1) {
			got = append(got, m[1])
		}
		want := render.Vocabulary(kind)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("spec table lists %v for %s, code emits %v", got, kind, want)
		}
	}
}
