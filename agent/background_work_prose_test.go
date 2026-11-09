package agent

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/bundled"
)

// backgroundWorkGolden renders the prose that teaches a session how a turn may
// end while work it started still runs. The per-kind Background work section
// and pre-end check are pinned one file per session kind; the kind-independent
// pieces go to shared goldens, since one wording change would otherwise read
// as three identical diffs.
func backgroundWorkGolden(section string) []byte {
	return []byte("## Background work\n" + section + "\n")
}

// backgroundWorkSharedGolden renders the two pieces that do not vary by
// session kind. The nudge is sent only to delegates; the role block reaches
// only sessions whose role is the bundled subagent.
func backgroundWorkSharedGolden(t *testing.T) []byte {
	t.Helper()
	var golden strings.Builder
	golden.WriteString("# communicateNudge\n\n" + communicateNudge("communicate") + "\n\n")
	role, err := fs.ReadFile(bundled.Agents(), "subagent.md")
	if err != nil {
		t.Fatalf("read the bundled subagent role: %v", err)
	}
	_, reporting, ok := strings.Cut(string(role), "\n## Reporting\n")
	if !ok {
		t.Fatal("the bundled subagent role has no Reporting section")
	}
	reporting, _, _ = strings.Cut(reporting, "\n## ")
	golden.WriteString("## Reporting (bundled subagent role)\n" + reporting + "\n")
	return []byte(golden.String())
}

// detachCapableEnv reports detach support unconditionally, so the Background
// work section's detached-mode paragraph renders the same on every host.
// detached_shell_prose_test.go owns that paragraph's two real shapes; here one
// pinned shape per golden is enough.
type detachCapableEnv struct{ execenv.ExecutionEnvironment }

func (detachCapableEnv) DetachSupported() bool { return true }

// pinBackgroundWorkEnvironment fixes the two Background work inputs that come
// from the host rather than the template: detach support and resource caps.
// Without both, this macOS host and CI's Linux runners render different
// sections from the same code.
func pinBackgroundWorkEnvironment(s *Session) execenv.ExecutionEnvironment {
	s.envInfo.Resources = &schema.ResourceCaps{CPUs: 2, MemoryMB: 4096}
	return detachCapableEnv{s.env}
}

// backgroundWorkSection cuts the Background work section — and the pre-end
// check, which lives at the template's tail — out of a rendered system prompt.
func backgroundWorkSection(t *testing.T, prompt string) string {
	t.Helper()
	_, section, ok := strings.Cut(prompt, "\n## Background work\n")
	if !ok {
		t.Fatal("system prompt has no Background work section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	_, tail, ok := strings.Cut(prompt, "\nBefore you end a turn, check where your answer is.")
	if !ok {
		t.Fatal("system prompt has no pre-end check")
	}
	tail, _, _ = strings.Cut(tail, "\n")
	return section + "\n\nBefore you end a turn, check where your answer is." + tail
}

// TestBackgroundWorkProseGoldens pins each session kind's ending-the-turn
// prose whole, so a wording change is read as a diff instead of passing behind
// a substring assertion.
func TestBackgroundWorkProseGoldens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		build func(*testing.T) (*Session, execenv.ExecutionEnvironment)
	}{
		{"root", func(t *testing.T) (*Session, execenv.ExecutionEnvironment) {
			s := buildRootInteractiveAnthropicSession(t)
			return s, pinBackgroundWorkEnvironment(s)
		}},
		{"delegate", func(t *testing.T) (*Session, execenv.ExecutionEnvironment) {
			s := buildPromptDelegate(t, 1, "")
			return s, pinBackgroundWorkEnvironment(s)
		}},
		{"oneshot", func(t *testing.T) (*Session, execenv.ExecutionEnvironment) {
			s := buildRootHeadlessCoordinatorSession(t)
			return s, pinBackgroundWorkEnvironment(s)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, env := tc.build(t)
			prompt, warning, _ := s.renderSystemPrompt(env)
			if warning != "" {
				t.Fatalf("render failed: %s", warning)
			}
			agenttest.CheckGolden(t, filepath.Join("testdata", "backgroundwork", tc.name+".md"),
				backgroundWorkGolden(backgroundWorkSection(t, prompt)), *updatePromptGoldens,
				"Regenerate with `go test ./agent -run 'TestBackgroundWorkProseGoldens$' -count=1 -update-prompt` and read the diff.")
		})
	}
	agenttest.CheckGolden(t, filepath.Join("testdata", "backgroundwork", "shared.md"),
		backgroundWorkSharedGolden(t), *updatePromptGoldens,
		"Regenerate with `go test ./agent -run 'TestBackgroundWorkProseGoldens$' -count=1 -update-prompt` and read the diff.")
}
