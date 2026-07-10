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
		for claude, serf := range claudeToSerf {
			if got := ClaudeToSerf(claude); got != serf {
				t.Fatalf("ClaudeToSerf(%q) = %q, want %q", claude, got, serf)
			}
			if got := SerfToClaude(serf); got != claude {
				t.Fatalf("SerfToClaude(ClaudeToSerf(%q)) = %q, want %q", claude, got, claude)
			}
		}
		if _, known := claudeToSerf[name]; !known && ClaudeToSerf(name) != name {
			t.Fatalf("unknown Claude tool %q did not pass through", name)
		}
		if _, known := serfToClaude[name]; !known && SerfToClaude(name) != name {
			t.Fatalf("unknown Serf tool %q did not pass through", name)
		}
	})
}
