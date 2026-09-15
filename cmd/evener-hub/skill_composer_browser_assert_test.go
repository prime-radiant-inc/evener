//go:build browserguard

package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillGuardTranscriptInputPairsExactTextAndMetadata(t *testing.T) {
	const text = "Run /skill-1 and then /skill-2"
	names := []string{"skill-1", "skill-2"}
	line := func(message, original string, skills []string) string {
		return skillGuardJSON(map[string]any{"kind": "entry", "turn": map[string]any{
			"kind": "USER_INPUT", "message": map[string]any{"content": []map[string]string{{"text": message}}},
			"skill_state": map[string]any{"input": map[string]any{"original_text": original, "names": skills}},
		}}) + "\n"
	}
	for _, tc := range []struct {
		name, data string
		wantOK     bool
	}{
		{"exact sentence and both names", line(text, text, names), true},
		{"detached slash labels", line("Run and then", text, names), false},
		{"moved references", line("Run and then /skill-1 /skill-2", text, names), false},
		{"extra text", line(text+" CHANGED", text, names), false},
		{"missing metadata", line(text, text, []string{"skill-1"}), false},
		{"wrong order", line(text, text, []string{"skill-2", "skill-1"}), false},
		{"different original", line(text, "Run and then", names), false},
		{"text and metadata on different turns", line(text, "other", nil) + line("other", text, names), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "sessions")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.transcript.jsonl"), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			err := skillGuardRequireTranscriptInput(t, root, text, names)
			if (err == nil) != tc.wantOK {
				t.Fatalf("RequireTranscriptInput error = %v, want success %v", err, tc.wantOK)
			}
		})
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
