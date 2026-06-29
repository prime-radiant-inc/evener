package schema

import (
	"bytes"
	"encoding/json"
	"testing"
)

// mustMarshalMeta marshals m or fails the test. SessionMeta has a custom
// UnmarshalJSON but default MarshalJSON; comparing marshaled bytes (not
// reflect.DeepEqual) keeps time.Time and pointer fields comparable.
func mustMarshalMeta(t *testing.T, m SessionMeta) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal session meta: %v", err)
	}
	return b
}

// FuzzSessionMetaRoundTrip drives the on-disk session-meta persistence seam
// (snapshot.go) with two oracles beyond no-panic:
//
//   - Decode→encode→decode FIXED POINT. The canonical (post-one-decode) bytes
//     must survive a second marshal/unmarshal unchanged. This proves
//     MarshalJSON/UnmarshalJSON agree across the full nested graph
//     (ConfigSnapshot + GoalSnapshot + maps/slices). The baseline is the
//     canonical form, not the raw input, which sidesteps the legacy
//     original_task→OriginalPrompt asymmetry (UnmarshalJSON reads it, MarshalJSON
//     does not write it).
//   - Save/Load ROUND-TRIP. SaveSessionMeta + LoadSessionMeta over a t.TempDir
//     must reproduce the canonical bytes. A dropped or mis-tagged field, or an
//     atomic-write/read regression, diverges here immediately.
func FuzzSessionMetaRoundTrip(f *testing.F) {
	seeds := []string{
		// Canonical compact meta with the common optional blocks populated.
		`{"id":"01ABC","profile_id":"openai","model":"gpt-5.5","cheap_model":"gpt-5.4-mini","config":{"max_tool_rounds_per_input":40,"max_turns":0,"tool_output_limits":{"shell":{"ride_whole_bytes":8192}},"agent_name":"engineer","reasoning_effort":"high","skills_dirs":["/a","/b"],"model_fallbacks":["anthropic/claude"],"enable_loop_detection":false},"env_info":{"working_dir":"/home/u/p"},"created_at":"2026-06-01T10:00:00Z","updated_at":"2026-06-01T11:00:00Z","turn_count":7,"last_input_tokens":12000,"name":"Session title","name_source":"prompt","name_updated_at":"2026-06-01T10:05:00Z","original_prompt":"do the thing","goal":{"objective":"ship it","status":"active","iterations":3,"made_progress_once":true,"created_at":"2026-06-01T10:00:00Z","updated_at":"2026-06-01T10:30:00Z"},"pinned_note":"remember this","observed_by":["01OBS1","01OBS2"]}`,
		// Fork lineage fields.
		`{"id":"01FORK","profile_id":"anthropic","model":"claude","config":{},"env_info":{},"created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z","turn_count":0,"parent_session_id":"01PARENT","divergence_turn":4,"fork_label":"main","is_subagent":true}`,
		// Legacy-only doc: pins the original_task→OriginalPrompt fallback path.
		`{"original_task":"legacy prompt only"}`,
		// Minimal / degenerate inputs.
		`{}`,
		`null`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var m1 SessionMeta
		if json.Unmarshal(raw, &m1) != nil {
			return // rejected input: nothing further to assert
		}

		// Fixed point on the canonical (post-one-decode) bytes.
		b1 := mustMarshalMeta(t, m1)
		var m2 SessionMeta
		if err := json.Unmarshal(b1, &m2); err != nil {
			t.Fatalf("canonical bytes failed to re-decode: %v\n bytes=%s", err, b1)
		}
		b2 := mustMarshalMeta(t, m2)
		if !bytes.Equal(b1, b2) {
			t.Fatalf("session meta marshal is not a fixed point:\n once=%s\n twice=%s", b1, b2)
		}

		// Persist round-trip. Force a safe, fixed id so the on-disk filename is
		// well-formed regardless of the fuzzed id; re-derive the baseline after
		// the override so the comparison is against the persisted shape.
		m1.ID = "fuzz"
		baseline := mustMarshalMeta(t, m1)
		dir := t.TempDir()
		if err := SaveSessionMeta(dir, m1); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		m3, err := LoadSessionMeta(dir, "fuzz")
		if err != nil {
			t.Fatalf("LoadSessionMeta: %v", err)
		}
		if got := mustMarshalMeta(t, m3); !bytes.Equal(baseline, got) {
			t.Fatalf("session meta save/load round-trip diverged:\n saved=%s\n loaded=%s", baseline, got)
		}
	})
}
