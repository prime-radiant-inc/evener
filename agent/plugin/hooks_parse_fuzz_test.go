package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// hooksFuzzPluginDir is the sentinel plugin root the hooks fuzzer expands
// placeholders against, so the oracle can assert no placeholder survived.
const hooksFuzzPluginDir = "/PLUGIN_ROOT_SENTINEL"

// FuzzParsePluginHooks drives parsePluginHooks (via parsePluginHooksDiagWithSource)
// and its helpers (dropHookMetaKeys, captureUnknownFields, expandPluginRootArgs)
// over arbitrary hook-config JSON in both the wrapper ({"hooks":{...}}) and direct
// (events at top level) shapes. This is serf's Claude-compatible hook classifier;
// only unit tests with fixed configs touched it, and FuzzPluginManifestParse /
// FuzzPluginLoad do not reach the hook parser.
//
// Oracles (never bare no-panic):
//   - classification soundness: every event in the returned hooks map is in
//     validHookEvents; every "unsupported" key is recognized-but-not-fired; every
//     "unknown" key is neither recognized nor a dropped meta key ("description" /
//     "$"-prefixed).
//   - the three buckets are disjoint.
//   - placeholder expansion is total: no RegisteredHook's Command, Prompt, or Args
//     still contains ${CLAUDE_PLUGIN_ROOT} / ${PLUGIN_ROOT} after parsing against a
//     sentinel plugin dir.
//   - timeout defaulting: a command or prompt handler never ends up with Timeout 0
//     (the parser defaults 0 to 60/30 for those types).
//   - indices are well-formed and determinism holds: re-parsing the same bytes
//     yields a byte-identical hooks map.
//
// SAFETY: pure JSON parse + string expansion. The fuzzer calls parsePluginHooks
// directly (not discoverPluginHooks), so it never reads a file or spawns a hook.
func FuzzParsePluginHooks(f *testing.F) {
	seeds := []string{
		`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/h.sh","args":["${PLUGIN_ROOT}/x","y"]}]}]}}`,
		`{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"prompt","prompt":"check ${CLAUDE_PLUGIN_ROOT}"}]}]}`,
		`{"hooks":{"$schema":"x","description":"d","Stop":[{"hooks":[{"type":"command","command":"c","timeout":5}]}]}}`,
		`{"WorktreeCreate":[{"hooks":[{"type":"command","command":"c"}]}],"TotallyMadeUp":[]}`,
		`{"SessionStart":[{"matcher":"resume","hooks":[{"type":"command","command":"c","mystery":true,"Command":"cased"}]}]}`,
		`{"PreToolUse":[{"hooks":[{"type":"command","timeout":0,"command":"x"}]}]}`,
		`{"hooks":"not-an-object"}`,
		`{}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, s := range edgeseeds.JSON() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		hooks, unsupported, unknown, err := parsePluginHooksDiagWithSource(data, hooksFuzzPluginDir, "p", "")
		if err != nil {
			// Rejected config must not leak partial buckets.
			if hooks != nil || unsupported != nil || unknown != nil {
				t.Fatalf("error result leaked non-nil buckets: %v", err)
			}
			return
		}

		for event, handlers := range hooks {
			if !validHookEvents[event] {
				t.Fatalf("hooks map contains non-fired event %q", event)
			}
			for hi, h := range handlers {
				if h.Event != event {
					t.Fatalf("handler %d under %q carries Event %q", hi, event, h.Event)
				}
				if h.GroupIndex < 0 || h.HandlerIndex < 0 {
					t.Fatalf("handler under %q has negative index", event)
				}
				assertNoPlaceholder(t, event, h.Command)
				assertNoPlaceholder(t, event, h.Prompt)
				for _, a := range h.Args {
					assertNoPlaceholder(t, event, a)
				}
				if (h.Type == "command" || h.Type == "prompt") && h.Timeout == 0 {
					t.Fatalf("handler under %q type %q kept Timeout 0 (should default)", event, h.Type)
				}
			}
		}

		for event := range unsupported {
			if !recognizedClaudeEvents[event] || validHookEvents[event] {
				t.Fatalf("unsupported bucket has misclassified event %q", event)
			}
			if _, dup := hooks[event]; dup {
				t.Fatalf("event %q in both hooks and unsupported", event)
			}
		}

		for name := range unknown {
			if recognizedClaudeEvents[HookEvent(name)] {
				t.Fatalf("unknown bucket has recognized event %q", name)
			}
			if name == "description" || strings.HasPrefix(name, "$") {
				t.Fatalf("unknown bucket has a meta key %q that should be dropped", name)
			}
			if _, dup := hooks[HookEvent(name)]; dup {
				t.Fatalf("event %q in both hooks and unknown", name)
			}
			if unsupported[HookEvent(name)] {
				t.Fatalf("event %q in both unsupported and unknown", name)
			}
		}

		// Determinism: a second parse yields a byte-identical hooks map.
		hooks2, _, _, err2 := parsePluginHooksDiagWithSource(data, hooksFuzzPluginDir, "p", "")
		if err2 != nil {
			t.Fatalf("second parse errored after first succeeded: %v", err2)
		}
		if a, b := mustJSON(t, hooks), mustJSON(t, hooks2); !bytes.Equal(a, b) {
			t.Fatalf("parse not deterministic:\n once=%s\n twice=%s", a, b)
		}
	})
}

func assertNoPlaceholder(t *testing.T, event HookEvent, s string) {
	t.Helper()
	if strings.Contains(s, "${CLAUDE_PLUGIN_ROOT}") || strings.Contains(s, "${PLUGIN_ROOT}") {
		t.Fatalf("unexpanded plugin-root placeholder under %q: %q", event, s)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
