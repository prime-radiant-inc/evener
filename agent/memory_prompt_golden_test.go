package agent

// The memory guidance and the memory tools' descriptions are prompt text: they
// are pinned whole, per capability shape, never by substring. Regenerate after
// an intended wording change with
//
//	go test ./agent -run 'TestMemoryPromptGolden$' -count=1 -update-prompt
//
// and read the diff.

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updatePromptGoldens = flag.Bool("update-prompt", false,
	"rewrite agent/testdata/memoryprompt from the current memory guidance and tool descriptions")

func TestMemoryPromptGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cfg    SessionConfig
		revoke string
	}{
		{"enabled", SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}, ""},
		{"personal-only", SessionConfig{MemoryStateRoot: t.TempDir()}, ""},
		{"read-only", SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}, "memory_write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withConfig(tc.cfg))
			if tc.revoke != "" {
				s.reg.Remove(tc.revoke)
				refreshModelFacingCaches(s)
			}
			prompt, warning := s.renderSystemPrompt(s.currentEnv())
			if warning != "" {
				t.Fatal(warning)
			}
			_, section, ok := strings.Cut(prompt, memoryGuidanceHeading)
			if !ok {
				t.Fatal("no memory section")
			}
			var out strings.Builder
			out.WriteString(strings.TrimPrefix(memoryGuidanceHeading, "\n\n") + section + "\n\n# Memory tool descriptions\n")
			for _, name := range nativeMemoryToolNames {
				if registered := s.reg.Get(name); registered != nil {
					fmt.Fprintf(&out, "\n## %s\n\n%s\n", name, registered.Definition.Description)
				}
			}
			path := filepath.Join("testdata", "memoryprompt", tc.name+".md")
			got := []byte(out.String())
			if *updatePromptGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (regenerate with -update-prompt)", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("memory prompt drifted from %s; regenerate with -update-prompt and read the diff.\ngot:\n%s", path, got)
			}
		})
	}
}
