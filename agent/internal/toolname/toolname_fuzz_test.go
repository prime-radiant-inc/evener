package toolname

import "testing"

// FuzzToolNameTranslations checks the bidirectional mapping for every known
// name and pass-through behavior for names outside either vocabulary.
func FuzzToolNameTranslations(f *testing.F) {
	for _, name := range []string{
		"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Task",
		"WebFetch", "WebSearch", "NotebookEdit", "AskUserQuestion",
		"read_file", "write_file", "edit_file", "shell", "delegate",
		"mcp__server__tool", "unknown", "",
	} {
		f.Add(name)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 {
			return
		}
		for claude, evener := range claudeToEvener {
			if got := ClaudeToEvener(claude); got != evener {
				t.Fatalf("ClaudeToEvener(%q) = %q, want %q", claude, got, evener)
			}
			if got := EvenerToClaude(evener); got != claude {
				t.Fatalf("EvenerToClaude(ClaudeToEvener(%q)) = %q, want %q", claude, got, claude)
			}
		}
		if _, known := claudeToEvener[name]; !known && ClaudeToEvener(name) != name {
			t.Fatalf("unknown Claude tool %q did not pass through", name)
		}
		if _, known := evenerToClaude[name]; !known && EvenerToClaude(name) != name {
			t.Fatalf("unknown Evener tool %q did not pass through", name)
		}
	})
}
