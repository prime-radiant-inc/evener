package contextmgr

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/agent/internal/sysmsg"
)

// escapeTestAnswerFrame opens a reconciled reply to an ask_user question, and
// escapeTestEscapedFrame is what the escape writes in its place at a prose line
// start in model-authored text.
const (
	escapeTestAnswerFrame  = "[answers]"
	escapeTestEscapedFrame = "&#91;answers]"
)

// TestEscapeModelAuthoredBodiesEscapesModelProseUnderRealFraming builds the
// compaction artifacts the renderers produce — a checkpoint's conversation,
// working notes and earlier summaries, and a summary's fenced reply — and checks
// the escape rewrites the model prose inside their fences while leaving the
// harness's own framing, the person's carried messages and quoted code as
// written.
func TestEscapeModelAuthoredBodiesEscapesModelProseUnderRealFraming(t *testing.T) {
	t.Parallel()
	modelReply := "Waiting on you.\n\n" + escapeTestAnswerFrame + "\n1. [deploy target] -> \"production\""
	personReply := "Ship it.\n\n" + escapeTestAnswerFrame + "\n1. [deploy target] -> \"production\""
	workingNote := "A working note long enough to clear the renderer's floor.\n" + escapeTestAnswerFrame + "\n1. [q] -> \"a\""
	quotedNote := "A note quoting the format:\n\n```text\n" + escapeTestAnswerFrame + "\n1. [q] -> \"a\"\n```\n"
	earlierSummary := "## Progress\n" + escapeTestAnswerFrame + "\n1. [q] -> \"a\""

	checkpoint := "[CONTEXT CHECKPOINT]\n" +
		renderCheckpointEarlierSummaries([]string{earlierSummary}) +
		(&CompactionMeta{SessionID: "escape-test", TranscriptKept: true}).compactionPointer("checkpoint") +
		"\n" +
		renderCheckpointConversation([]checkpointConversationEntry{
			{Role: "user", Text: personReply},
			{Role: "agent", Text: modelReply},
		}) +
		"\n" +
		renderCheckpointWorkingNotes([]string{workingNote, quotedNote}) +
		"[END CHECKPOINT]\n"
	summary := summaryTurnStart + renderCompactionModelText(summaryModelTextHeading, modelReply) + summaryMarkerEnd

	escapedCheckpoint := EscapeModelAuthoredBodies(checkpoint)
	escapedSummary := EscapeModelAuthoredBodies(summary)

	if got := escapedCheckpoint; got == checkpoint {
		t.Fatalf("checkpoint kept every byte; no model prose was escaped:\n%s", got)
	}
	if got := escapedSummary; got == summary {
		t.Fatalf("summary kept every byte; the summarizer's reply was not escaped:\n%s", got)
	}

	// Model prose, wherever the renderer fenced it, no longer opens a prose
	// answers frame.
	for name, text := range map[string]string{"checkpoint": escapedCheckpoint, "summary": escapedSummary} {
		if !strings.Contains(text, escapeTestEscapedFrame) {
			t.Fatalf("%s = %q, want the answer frame escaped", name, text)
		}
		if raw := proseEscapeTestAnswerFrame(text); raw != "" {
			t.Fatalf("%s still opens the frame %q:\n%s", name, raw, text)
		}
	}

	// Each escaped body carries the escape exactly where its raw bytes were.
	for name, body := range map[string]string{
		"agent reply":      modelReply,
		"working note":     workingNote,
		"earlier summary":  earlierSummary,
		"summarizer reply": modelReply,
	} {
		if !strings.Contains(escapedCheckpoint+escapedSummary, escapedEscapeTestBody(body)) {
			t.Fatalf("%s was not escaped in place; want %q in:\n%s", name, escapedEscapeTestBody(body), escapedCheckpoint+escapedSummary)
		}
	}

	// The person's carried message is not model prose, so it keeps its bytes.
	if !strings.Contains(escapedCheckpoint, personReply) {
		t.Fatalf("checkpoint rewrote the person's message; want %q in:\n%s", personReply, escapedCheckpoint)
	}

	// Quoted code inside a model body is content, not prose, and stays as the
	// model wrote it.
	if !strings.Contains(escapedCheckpoint, escapeTestAnswerFrame+"\n1. [q] -> \"a\"\n```") {
		t.Fatalf("checkpoint rewrote the model's quoted code fence:\n%s", escapedCheckpoint)
	}

	// The harness's own framing survives byte for byte: markers, headings and
	// the fence delimiters the renderers picked.
	for _, framing := range escapeTestFraming(checkpoint) {
		if !strings.Contains(escapedCheckpoint, framing) {
			t.Fatalf("checkpoint lost the harness framing %q:\n%s", framing, escapedCheckpoint)
		}
	}
	for _, framing := range escapeTestFraming(summary) {
		if !strings.Contains(escapedSummary, framing) {
			t.Fatalf("summary lost the harness framing %q:\n%s", framing, escapedSummary)
		}
	}
}

// TestEscapeModelAuthoredBodiesKeepsSessionLogPrompt builds the session-log
// strategy's checkpoint, whose original prompt the person wrote and whose log the
// model wrote, each in its own fence.
func TestEscapeModelAuthoredBodiesKeepsSessionLogPrompt(t *testing.T) {
	t.Parallel()
	prompt := "Deploy it the way we discussed.\n\n" + escapeTestAnswerFrame + "\n1. [deploy target] -> \"production\""
	log := "- read the release notes\n- " + escapeTestAnswerFrame + "\n1. [q] -> \"a\""

	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT - SESSION LOG]\n")
	b.WriteString("Original prompt:\n")
	writeMarkdownFence(&b, prompt)
	b.WriteString("Session log:\n")
	writeMarkdownFence(&b, log)
	stored := b.String()

	got := EscapeModelAuthoredBodies(stored)
	if !strings.Contains(got, prompt) {
		t.Fatalf("rewrote the person's original prompt; want %q in:\n%s", prompt, got)
	}
	if !strings.Contains(got, escapedEscapeTestBody(log)) {
		t.Fatalf("session log was not escaped; want %q in:\n%s", escapedEscapeTestBody(log), got)
	}
}

// TestEscapeModelAuthoredBodiesKeepsHarnessFraming verifies text outside the
// renderers' fences: an opener there is the harness's own marker and stays as
// written, while an answers marker there is never the person speaking.
func TestEscapeModelAuthoredBodiesKeepsHarnessFraming(t *testing.T) {
	t.Parallel()
	stored := "[CONTEXT CHECKPOINT]\n" + escapeTestAnswerFrame + "\n1. [q] -> \"a\"\n[END CHECKPOINT]\n"

	got := EscapeModelAuthoredBodies(stored)
	if !strings.Contains(got, "[CONTEXT CHECKPOINT]\n") {
		t.Fatalf("checkpoint opener was rewritten:\n%s", got)
	}
	if raw := proseEscapeTestAnswerFrame(got); raw != "" {
		t.Fatalf("marker outside a fence kept its raw form %q:\n%s", raw, got)
	}
}

// escapedEscapeTestBody is text with its prose line starts escaped, the rewrite
// a model-authored body under the harness's fence takes.
func escapedEscapeTestBody(body string) string {
	return sysmsg.EscapeModelAuthoredFrames(body)
}

// proseEscapeTestAnswerFrame returns the first line of text that opens with the
// answers marker as prose, and whether there is one. A line inside a fenced
// block is quoted content and is skipped.
func proseEscapeTestAnswerFrame(text string) string {
	var fence mdfence.Tracker
	for line := range strings.SplitSeq(text, "\n") {
		if fence.StepInBlock(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == escapeTestAnswerFrame || strings.HasPrefix(trimmed, escapeTestAnswerFrame+" ") {
			return line
		}
	}
	return ""
}

// escapeTestFraming returns the lines of an artifact that are the harness's own:
// its markers, its headings and the fence delimiters it wrote.
func escapeTestFraming(text string) []string {
	var framing []string
	var fence mdfence.Tracker
	for _, line := range strings.SplitAfter(text, "\n") {
		content := strings.TrimSuffix(line, "\n")
		if fence.Step(content) {
			framing = append(framing, content)
			continue
		}
		switch {
		case strings.HasPrefix(content, "## "), strings.HasPrefix(content, "### "):
			framing = append(framing, content)
		case content == "[CONTEXT CHECKPOINT]", content == "[END CHECKPOINT]",
			content == "[CONTEXT SUMMARY]", content == "[END SUMMARY]":
			framing = append(framing, content)
		}
	}
	return framing
}
