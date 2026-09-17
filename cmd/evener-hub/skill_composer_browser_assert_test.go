//go:build browserguard

package hub

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func TestSkillGuardTranscriptInputPairsExactTextAndMetadata(t *testing.T) {
	const text = "Run /skill-1 and then /skill-2"
	names := []string{"skill-1", "skill-2"}
	line := func(kind schema.TurnKind, message, original string, skills []string) string {
		return skillGuardJSON(map[string]any{"kind": "entry", "turn": map[string]any{
			"kind": kind, "message": map[string]any{"content": []map[string]string{{"text": message}}},
			"skill_state": map[string]any{"input": map[string]any{"original_text": original, "names": skills}},
		}}) + "\n"
	}
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnSteering} {
		otherKind := schema.TurnSteering
		if kind == schema.TurnSteering {
			otherKind = schema.TurnUserInput
		}
		for _, tc := range []struct {
			name, data string
			wantOK     bool
		}{
			{"exact sentence and both names", line(kind, text, text, names), true},
			{"detached slash labels", line(kind, "Run and then", text, names), false},
			{"moved references", line(kind, "Run and then /skill-1 /skill-2", text, names), false},
			{"extra text", line(kind, text+" CHANGED", text, names), false},
			{"missing metadata", line(kind, text, text, []string{"skill-1"}), false},
			{"wrong order", line(kind, text, text, []string{"skill-2", "skill-1"}), false},
			{"different original", line(kind, text, "Run and then", names), false},
			{"text and metadata on different turns", line(kind, text, "other", nil) + line(kind, "other", text, names), false},
			{"wrong kind", line(otherKind, text, text, names), false},
			{"correct data only on another kind", line(kind, text, "other", nil) + line(otherKind, text, text, names), false},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				dir := filepath.Join(root, "sessions")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "fixture.transcript.jsonl"), []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
				err := skillGuardRequireTranscriptInput(t, root, text, names, kind)
				if (err == nil) != tc.wantOK {
					t.Fatalf("RequireTranscriptInput error = %v, want success %v", err, tc.wantOK)
				}
			})
		}
	}
}

func TestSkillGuardSkillContextsReadsEveryDocument(t *testing.T) {
	first := skillGuardDocument{Name: "skill-1", Instructions: "OPAQUE_1"}
	second := skillGuardDocument{Name: "skill-2", Instructions: "OPAQUE_2"}
	call := skillGuardLLMCall{Messages: []skillGuardMessage{{Role: "user", Content: []skillGuardLLMPart{{Kind: "text", Text: "<skill-context>" + skillGuardJSON(first) + "</skill-context>\n<skill-context>" + skillGuardJSON(second) + "</skill-context>"}}}}}
	got := call.skillContexts()
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("skill contexts = %#v, want both complete documents in order", got)
	}
}

func TestSkillGuardLastUserTextSkipsOnlyCompleteSkillEnvelopes(t *testing.T) {
	message := func(role, text string) skillGuardMessage {
		return skillGuardMessage{Role: role, Content: []skillGuardLLMPart{{Kind: "text", Text: text}}}
	}
	envelope := func(name string) string {
		return "<skill-context>\n" + skillGuardJSON(skillGuardDocument{
			Name: name, Description: "FIXTURE_DESCRIPTION", Source: "/skills/" + name + "/SKILL.md",
			BaseDirectory: "/skills/" + name, Instructions: "FIXTURE_BODY",
		}) + "\n</skill-context>"
	}
	const current = "Run /skill-1 and then /skill-2"
	first, second := envelope("skill-1"), envelope("skill-2")
	base := []skillGuardMessage{message("user", "OLDER_INPUT"), message("assistant", "OLDER_REPLY"), message("user", current)}
	for _, tc := range []struct {
		name string
		tail []skillGuardMessage
		want string
	}{
		{"current input", nil, current},
		{"two separate envelopes", []skillGuardMessage{message("user", first), message("user", second)}, current},
		{"two envelopes in one carrier", []skillGuardMessage{message("user", first+"\n"+second)}, current},
		{"later input excludes earlier history", []skillGuardMessage{message("user", first), message("user", second), message("assistant", "REPLY"), message("user", "LATER_INPUT"), message("user", first)}, "LATER_INPUT"},
		{"empty later input still excludes history", []skillGuardMessage{message("user", ""), message("user", first)}, ""},
		{"assistant is not input", []skillGuardMessage{message("assistant", second)}, current},
		{"malformed JSON stays operative", []skillGuardMessage{message("user", "<skill-context>{broken}</skill-context>")}, "<skill-context>{broken}</skill-context>"},
		{"incomplete document stays operative", []skillGuardMessage{message("user", "<skill-context>{\"name\":\"literal\"}</skill-context>")}, "<skill-context>{\"name\":\"literal\"}</skill-context>"},
		{"null stays operative", []skillGuardMessage{message("user", "<skill-context>null</skill-context>")}, "<skill-context>null</skill-context>"},
		{"missing close stays operative", []skillGuardMessage{message("user", "<skill-context>"+skillGuardJSON(skillGuardDocument{Name: "literal"}))}, "<skill-context>" + skillGuardJSON(skillGuardDocument{Name: "literal"})},
		{"prefix prose stays operative", []skillGuardMessage{message("user", "QUOTE "+first)}, "QUOTE " + first},
		{"suffix prose stays operative", []skillGuardMessage{message("user", first+" QUESTION")}, first + " QUESTION"},
		{"image-bearing input stays operative", []skillGuardMessage{{Role: "user", Content: []skillGuardLLMPart{{Kind: "text", Text: first}, {Kind: "image", Image: &skillGuardImage{Data: []byte{1}}}}}}, first},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := skillGuardLLMCall{Messages: append(append([]skillGuardMessage{}, base...), tc.tail...)}
			if got := call.lastUserText(); got != tc.want {
				t.Fatalf("lastUserText = %q, want %q", got, tc.want)
			}
		})
	}
}
