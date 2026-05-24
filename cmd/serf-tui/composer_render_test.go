package main

import (
	"strings"
	"testing"
)

func TestComposerChipStripShowsChips(t *testing.T) {
	got := renderComposerChipStrip(composerContext{
		Harness:    "serf",
		Model:      "openai/gpt-5.5",
		Branch:     "feat/widget",
		WorkingDir: "/home/jesse/git/serf",
		Width:      80,
	})
	for _, want := range []string{"harness serf", "model gpt-5.5", "branch feat/widget"} {
		if !strings.Contains(got, want) {
			t.Errorf("composer chip strip missing %q in: %q", want, got)
		}
	}
}

func TestComposerChipStripIncludesModeChip(t *testing.T) {
	got := renderComposerChipStrip(composerContext{
		Harness: "serf",
		Mode:    "QUEUE 2",
		Width:   80,
	})
	if !strings.Contains(got, "QUEUE 2") {
		t.Errorf("composer should include mode chip: %q", got)
	}
}

func TestComposerFooterHintsAreModeAware(t *testing.T) {
	compose := composerFooterHints("compose", 100)
	if !strings.Contains(compose, "send") {
		t.Errorf("compose mode footer should include send: %q", compose)
	}
	queue := composerFooterHints("queue", 100)
	if !strings.Contains(queue, "queue") {
		t.Errorf("queue mode footer should include queue: %q", queue)
	}
	fork := composerFooterHints("fork", 100)
	if !strings.Contains(fork, "fork") {
		t.Errorf("fork mode footer should include fork: %q", fork)
	}
}
