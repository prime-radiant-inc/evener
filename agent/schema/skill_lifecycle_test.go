package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Removing lifecycle metadata from the real persistence boundary loses this
// independent wire fixture, including explicit false controls and obligations.
func TestSkillLifecycleSnapshot_MetaRoundTrip(t *testing.T) {
	const lifecycle = `{"revision":7,"inventory":{"scope:alpha":{"ordinary":{"identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"description":"fixture-description","controls":{"disable_model_invocation":false,"user_invocable":false},"route":"user_slash","invocation_id":"inv-1","user_authorized":true},"preload":{"name":"scope:alpha","description":"frozen-description","source":"/fixture/frozen/SKILL.md","file_digest":"frozen-file","rendered_digest":"frozen-render"}}},"obligations":[{"invocation_id":"inv-1","tool_call_id":"tool-1","client_mutation_id":"mutation-1","atomic_group_id":"group-1","identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"route":"user_slash"}],"pinned_note_gen":19,"next_operation_gen":23}`
	var meta SessionMeta
	if err := json.Unmarshal([]byte(`{"id":"lifecycle-fixture","pinned_note":"opaque-note","skills":`+lifecycle+`}`), &meta); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	var wantSkills, gotSkills any
	if err := json.Unmarshal([]byte(lifecycle), &wantSkills); err != nil {
		t.Fatal(err)
	}
	if raw, ok := wire["skills"]; ok {
		if err := json.Unmarshal(raw, &gotSkills); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(gotSkills, wantSkills) {
		t.Fatalf("lifecycle metadata lost or changed by SaveSessionMeta/LoadSessionMeta: got=%s want=%s", wire["skills"], lifecycle)
	}
	if got.PinnedNote != "opaque-note" {
		t.Fatalf("pinned note=%q", got.PinnedNote)
	}
}

// A missing or incorrectly tagged Turn.SkillState field must not silently drop
// input, provisional outcomes or their still-outstanding obligations.
func TestSkillLifecycleSnapshot_TurnRecordsRoundTrip(t *testing.T) {
	const fixture = `{"input":{"original_text":"opaque-original","arguments":"opaque-arguments","names":["scope:alpha"],"atomic_group_id":"group-1"},"outcomes":[{"revision":7,"session_id":"session-1","invocation_id":"inv-1","tool_call_id":"tool-1","client_mutation_id":"mutation-1","identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"activation":{"identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"description":"fixture-description","controls":{"disable_model_invocation":false,"user_invocable":false},"route":"user_slash","invocation_id":"inv-1","user_authorized":true},"status":"already_present","error_code":"","previous_identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"old-file","rendered_digest":"old-render"},"previous_controls":{"disable_model_invocation":false,"user_invocable":false}}],"obligations":[{"invocation_id":"inv-1","tool_call_id":"tool-1","client_mutation_id":"mutation-1","atomic_group_id":"group-1","identity":{"name":"scope:alpha","declared_name":"alpha","source":"/fixture/alpha/SKILL.md","file_digest":"file-1","rendered_digest":"render-1"},"route":"user_slash"}]}`
	var turn Turn
	if err := json.Unmarshal([]byte(`{"kind":"USER_INPUT","skill_state":`+fixture+`}`), &turn); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(fixture), &want); err != nil {
		t.Fatal(err)
	}
	if raw, ok := wire["skill_state"]; ok {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed turn state lost or changed: got=%s want=%s", wire["skill_state"], fixture)
	}
}
