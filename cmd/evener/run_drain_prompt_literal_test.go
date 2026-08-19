package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// The system prompt is text in the request, so a prompt section that spells a
// wire frame literally is indistinguishable from a delivered frame to anything
// matching on substrings (kata zzpw). These tests inject such a literal through
// the real --system-prompt-append path and assert the drain still works, so a
// prompt author can name a frame without breaking an end-to-end test.
const (
	delegateFramePromptLiteral = `<delegate-notification delegate_id="x">`
	jobFramePromptLiteral      = `<job-notification job_id="x">`
)

// promptLayouts covers both request layouts buildModelRequest can produce. Under
// SystemPromptAsUser the prompt is fused into the opening user turn instead of
// getting its own system-role message, so a fix that merely filters system-role
// messages would still leak the literal in the second case.
var promptLayouts = []struct {
	name   string
	asUser bool
}{
	{name: "system role message", asUser: false},
	{name: "fused into opening user turn", asUser: true},
}

// writeSystemPromptAppend writes a --system-prompt-append file carrying text and
// returns its path. Prompt assembly silently skips an unreadable append path
// (agent/session_prompts.go), so every caller must also confirm the text
// actually reached the request; assertPromptLiteralReachedOpeningMessage is how.
func writeSystemPromptAppend(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wire-literal-section.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write system prompt append: %v", err)
	}
	return path
}

// wireLiteralSection is prompt prose naming a frame the way a real section
// under agent/prompts/sections/ would.
func wireLiteralSection(literal string) string {
	return "When the work finishes, its result reaches you as an ordinary terminal\n" +
		"`" + literal + "` frame carrying the result packet.\n"
}

// namePromptLiteral installs the wire literal as an appended prompt section and
// selects the request layout under test.
func namePromptLiteral(t *testing.T, literal string, asUser bool) func(*runConfig) {
	t.Helper()
	path := writeSystemPromptAppend(t, wireLiteralSection(literal))
	return func(cfg *runConfig) {
		cfg.systemPromptAppend = []string{path}
		cfg.systemPromptAsUser = asUser
	}
}

// assertPromptLiteralReachedOpeningMessage fails unless the literal really is in
// the assembled system prompt, in message 0. Without it a mistyped append path
// would leave these tests passing vacuously, proving nothing about the hazard.
func assertPromptLiteralReachedOpeningMessage(t *testing.T, adapter *scriptedProvider, literal string) {
	t.Helper()
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("scripted provider saw no requests")
	}
	if len(reqs[0].Messages) == 0 {
		t.Fatal("first request had no messages")
	}
	if !strings.Contains(reqs[0].Messages[0].Text(), literal) {
		t.Fatalf("wire literal %q never reached the system prompt in message 0, so this test proves nothing; message 0 (role %q) opens %s",
			literal, reqs[0].Messages[0].Role, headString(reqs[0].Messages[0].Text(), 200))
	}
}

// headString renders the opening of s for a failure message. The assembled
// system prompt is tens of kilobytes; a failure that dumps it whole buries the
// line that explains it.
func headString(s string, n int) string {
	if len(s) <= n {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:n]) + "... (" + strconv.Itoa(len(s)) + " bytes)"
}

// TestRunDrainsDelegatedJobTreeWhenSystemPromptNamesTheFrame is the zzpw
// acceptance: the delegate drain must survive a prompt section that spells
// <delegate-notification> literally. Before the fix the coordinator's fake LLM
// matched the literal on its very first round, answered as though a delegate had
// already completed, and never dispatched one.
func TestRunDrainsDelegatedJobTreeWhenSystemPromptNamesTheFrame(t *testing.T) {
	for _, layout := range promptLayouts {
		t.Run(layout.name, func(t *testing.T) {
			adapter := delegateDrainScenario(t, namePromptLiteral(t, delegateFramePromptLiteral, layout.asUser))
			assertPromptLiteralReachedOpeningMessage(t, adapter, delegateFramePromptLiteral)
		})
	}
}

// TestRunDrainsManagedShellWhenSystemPromptNamesTheJobFrame is the same hazard
// on the job frame, which the kata does not name. The managed-shell steps both
// assert on and count "<job-notification", so a prompt literal there shifts a
// count by one rather than losing a dispatch.
func TestRunDrainsManagedShellWhenSystemPromptNamesTheJobFrame(t *testing.T) {
	for _, layout := range promptLayouts {
		t.Run(layout.name, func(t *testing.T) {
			adapter := managedShellDrainScenario(t, "printf shell-ok", "completed",
				namePromptLiteral(t, jobFramePromptLiteral, layout.asUser))
			assertPromptLiteralReachedOpeningMessage(t, adapter, jobFramePromptLiteral)
		})
	}
}

// TestRunDrainCountsOnlyDeliveredJobFramesWhenSystemPromptNamesOne guards the
// five running-count assertions in the chained-shell drain. Their failure mode
// is the worse one: a prompt literal shifts every count by one, so the drain
// still runs and the test still measures it, just wrongly. Nothing else covers
// them — no prompt section names <job-notification>, so reverting those five
// matchers to requestFullText leaves the whole drain suite green.
func TestRunDrainCountsOnlyDeliveredJobFramesWhenSystemPromptNamesOne(t *testing.T) {
	for _, layout := range promptLayouts {
		t.Run(layout.name, func(t *testing.T) {
			adapter := chainedShellDrainScenario(t, namePromptLiteral(t, jobFramePromptLiteral, layout.asUser))
			assertPromptLiteralReachedOpeningMessage(t, adapter, jobFramePromptLiteral)
		})
	}
}

// TestSystemPromptOccupiesOnlyTheOpeningMessage pins the invariant that
// requestDeliveredText rests on: buildModelRequest (agent/session_model_call.go)
// puts the whole system prompt in Messages[0] and nowhere else, in both request
// layouts. requestDeliveredText skips the first message, not "the system
// prompt"; those coincide only while this holds. If a future assembly path ever
// left message 0 carrying real conversation instead, a delivery matcher would
// silently ignore a genuinely delivered frame — a test passing when it should
// fail, which is the same false green kata zzpw is about. This test is the loud
// tripwire for that, and it uses a sentinel of its own rather than any prompt
// wording, so editing a prompt section cannot break it.
func TestSystemPromptOccupiesOnlyTheOpeningMessage(t *testing.T) {
	const sentinel = "ZZPW-SYSTEM-PROMPT-SENTINEL"
	for _, layout := range promptLayouts {
		t.Run(layout.name, func(t *testing.T) {
			adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return scriptedCommunicate("done") },
			}}
			installRunScriptedProvider(t, adapter)

			var stdout, stderr bytes.Buffer
			err := run(context.Background(), runConfig{
				prompt:             "answer immediately",
				model:              "openai/gpt-test",
				workDir:            t.TempDir(),
				systemPromptAppend: []string{writeSystemPromptAppend(t, sentinel+"\n")},
				systemPromptAsUser: layout.asUser,
				stdout:             &stdout,
				stderr:             &stderr,
			})
			if err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}

			reqs := adapter.Requests()
			if len(reqs) == 0 {
				t.Fatal("scripted provider saw no requests")
			}
			for i, req := range reqs {
				if len(req.Messages) == 0 {
					t.Fatalf("request %d had no messages", i)
				}
				if !strings.Contains(req.Messages[0].Text(), sentinel) {
					t.Fatalf("request %d: system prompt is not in message 0 (role %q); requestDeliveredText skips message 0 to exclude the prompt, so it now excludes the wrong message and lets prompt text back into a delivery match. message 0 opens %s",
						i, req.Messages[0].Role, headString(req.Messages[0].Text(), 200))
				}
				for j, m := range req.Messages[1:] {
					if strings.Contains(m.Text(), sentinel) {
						t.Fatalf("request %d: system prompt also appears in message %d (role %q); requestDeliveredText cannot exclude the prompt by skipping message 0 alone",
							i, j+1, m.Role)
					}
				}
			}
		})
	}
}
