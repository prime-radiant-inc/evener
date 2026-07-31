package main

import (
	"fmt"
	"strings"
	"testing"
)

// realisticChipStripContext mirrors kata wqyx's own repro setup (harness,
// provider/model, a long working dir, connected, a retry in flight, and a
// QUEUE mode chip) — the combination that measured "every right-side signal
// gone" at width 80. WorkingDir is ~60 columns, matching the kata's
// measurement.
func realisticChipStripContext(width int) composerContext {
	return composerContext{
		Harness:    "serf",
		Provider:   "openai",
		Model:      "openai/gpt-5.5",
		WorkingDir: "/Users/jesse/prime-radiant/toil-suite/serf/webui-workspace-shell",
		Connected:  true,
		HubAddr:    "http://127.0.0.1:8420",
		Mode:       "QUEUE 2",
		Retry:      "rate limited · retry 2/5 · 4s",
		Width:      width,
	}
}

// kata wqyx phase 2: the truncation policy kept the left side (harness,
// model, branch, working dir) whole and gave the right side (connection
// status, retry chip, mode chip) whatever remained — often nothing. The
// working-dir path is static context the user already knows; the right side
// is live state, and the retry chip specifically exists to explain a
// session that looks hung. All three right-side signals must survive at
// every width the kata measured.
func TestComposerChipStripRightSideSurvivesNarrowWidths(t *testing.T) {
	withTestColorProfile(t)
	for _, width := range []int{80, 120, 160, 200} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			got := renderComposerChipStrip(realisticChipStripContext(width))
			plain := ansiPattern.ReplaceAllString(got, "")
			if !strings.Contains(plain, "connected") {
				t.Errorf("connection status did not survive at width %d: %q", width, plain)
			}
			if !strings.Contains(plain, "rate limited") {
				t.Errorf("retry chip did not survive at width %d: %q", width, plain)
			}
			if !strings.Contains(plain, "QUEUE") {
				t.Errorf("mode chip did not survive at width %d: %q", width, plain)
			}
		})
	}
}

// kata wqyx phase 2 degradation order: the working-dir path is the first
// thing to yield (it's static context the user already knows), ahead of any
// right-side content. At width 80 it should be dropped outright rather than
// eating the room the retry/mode/connection chips need; once the row is
// wide enough to afford it (200, comfortably above the ~60-col path plus
// the fixed chips), it should reappear.
func TestComposerChipStripDropsWorkingDirBeforeRightSide(t *testing.T) {
	withTestColorProfile(t)
	// AbbreviatePath keeps a 32-column cap even when width is generous (a
	// display cap, not just a narrow-width fallback), so at 200 columns only
	// the tail of the path survives middle-truncation. "workspace-shell" is
	// within that tail; the full "webui-workspace-shell" is not.
	const dirNeedle = "workspace-shell"

	narrow := renderComposerChipStrip(realisticChipStripContext(80))
	plainNarrow := ansiPattern.ReplaceAllString(narrow, "")
	if strings.Contains(plainNarrow, dirNeedle) {
		t.Errorf("working dir should be dropped at width 80 to leave room for live state: %q", plainNarrow)
	}
	for _, want := range []string{"connected", "rate limited", "QUEUE"} {
		if !strings.Contains(plainNarrow, want) {
			t.Errorf("width 80: right side should survive even with the dir dropped, missing %q in: %q", want, plainNarrow)
		}
	}

	wide := renderComposerChipStrip(realisticChipStripContext(200))
	plainWide := ansiPattern.ReplaceAllString(wide, "")
	if !strings.Contains(plainWide, dirNeedle) {
		t.Errorf("working dir should reappear once width allows: %q", plainWide)
	}
}

func TestComposerChipStripShowsChips(t *testing.T) {
	withTestColorProfile(t)
	got := renderComposerChipStrip(composerContext{
		Harness:    "serf",
		Model:      "openai/gpt-5.5",
		Branch:     "feat/widget",
		WorkingDir: "/home/jesse/git/serf",
		Width:      80,
	})
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"harness serf", "model gpt-5.5", "branch feat/widget"} {
		if !strings.Contains(plain, want) {
			t.Errorf("composer chip strip missing %q in: %q", want, plain)
		}
	}
}

func TestComposerChipStripIncludesModeChip(t *testing.T) {
	withTestColorProfile(t)
	got := renderComposerChipStrip(composerContext{
		Harness: "serf",
		Mode:    "queue 2",
		Width:   80,
	})
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "QUEUE 2") {
		t.Errorf("composer should include uppercased mode chip: %q", plain)
	}
}

func TestComposerFooterHintsAreModeAware(t *testing.T) {
	compose := composerFooterHints("compose", 100, false)
	if !strings.Contains(compose, "send") {
		t.Errorf("compose mode footer should include send: %q", compose)
	}
	queue := composerFooterHints("queue", 100, false)
	if !strings.Contains(queue, "queue") {
		t.Errorf("queue mode footer should include queue: %q", queue)
	}
	fork := composerFooterHints("fork", 100, false)
	if !strings.Contains(fork, "fork") {
		t.Errorf("fork mode footer should include fork: %q", fork)
	}
}

func TestComposerFooterHintsQueueCanSteer(t *testing.T) {
	withSteer := composerFooterHints("queue", 100, true)
	if !strings.Contains(withSteer, "steer") {
		t.Errorf("queue mode with canSteer=true should include steer hint: %q", withSteer)
	}
	withoutSteer := composerFooterHints("queue", 100, false)
	if strings.Contains(withoutSteer, "steer") {
		t.Errorf("queue mode with canSteer=false should not include steer hint: %q", withoutSteer)
	}
}
