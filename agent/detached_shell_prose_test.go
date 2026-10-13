package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
)

// TestDetachedShellProseGoldens pins the model-facing prose about a detached
// shell command's environment (#4331) whole, per the prose rule in
// docs/developing-evener/testing.md: the shell tool's mode description, which a
// model reads when it chooses to detach, and the escalation that tells it to
// relaunch a job detached. Both state the same contract — output is discarded,
// the session scratch is absent, give absolute paths — so a wording change has
// to be a reviewed diff rather than silent drift. Regenerate with
//
//	go test ./agent -run 'TestDetachedShellProseGoldens$' -count=1 -update-prompt
//
// and read the diff.
func TestDetachedShellProseGoldens(t *testing.T) {
	t.Parallel()
	const hint = "Regenerate with `go test ./agent -run 'TestDetachedShellProseGoldens$' -count=1 -update-prompt` and read the diff."

	props, _ := tool.DefShell().Parameters["properties"].(map[string]any)
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatal("shell schema has no mode property")
	}
	description, _ := mode["description"].(string)
	agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", "mode-description.md"), []byte(description+"\n"), *updatePromptGoldens, hint)

	announcement := undisposedBackgroundJobsAnnouncement([]string{"job_host", "job_build"}, "shell", true)
	agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", "undisposed-announcement.md"), []byte(announcement+"\n"), *updatePromptGoldens, hint)
}
