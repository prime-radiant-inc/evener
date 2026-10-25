package agent

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

// shellModeProperty returns the shell schema's mode property.
func shellModeProperty(t *testing.T, params map[string]any) map[string]any {
	t.Helper()
	props, _ := params["properties"].(map[string]any)
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatalf("shell schema has no mode property: %#v", params)
	}
	return mode
}

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

	mode := shellModeProperty(t, tool.DefShell().Parameters)
	description, _ := mode["description"].(string)
	agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", "mode-description.md"), []byte(description+"\n"), *updatePromptGoldens, hint)

	sandboxedMode := shellModeProperty(t, tool.DefShellWithDetach(false).Parameters)
	sandboxedDescription, _ := sandboxedMode["description"].(string)
	agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", "mode-description-unavailable.md"), []byte(sandboxedDescription+"\n"), *updatePromptGoldens, hint)

	announcement := undisposedBackgroundJobsAnnouncement([]string{"job_host", "job_build"}, "shell", true)
	agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", "undisposed-announcement.md"), []byte(announcement+"\n"), *updatePromptGoldens, hint)

	// The system prompt's Background work paragraph is model-facing prose too:
	// #4922 requires its detach sentence to follow the environment's capability
	// like the shell schema does. One golden is a detach-capable delegate, the
	// other a sandboxed delegate whose environment refuses DetachCommand.
	for _, tc := range []struct {
		name    string
		capable bool
		build   func(t *testing.T) *Session
	}{
		{"background-work-detach-capable", true, func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "") }},
		{"background-work-sandboxed", false, func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "explorer") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := tc.build(t)
			if tc.capable && !detachSupportedBy(sess.env) {
				if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
					t.Skip("this platform cannot disown a process, so the capable golden does not apply")
				}
				t.Fatal("the capable delegate's environment cannot detach on a platform that supports it")
			}
			// Resource caps are observed only on some hosts, and the section
			// renders a caps paragraph only when they exist. Pin them so the
			// same golden holds on every host.
			sess.envInfo.Resources = &schema.ResourceCaps{CPUs: 2, MemoryMB: 4096}
			prompt, warning := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			agenttest.CheckGolden(t, filepath.Join("testdata", "detachedshell", tc.name+".md"),
				[]byte(systemPromptSection(t, prompt, "## Background work")), *updatePromptGoldens, hint)
		})
	}
}

// systemPromptSection returns the section heading opens, through but not
// including the next top-level heading, so a golden pins one template section
// whole rather than asserting a phrase inside it.
func systemPromptSection(t *testing.T, prompt, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(prompt, "\n"+heading+"\n")
	if !ok {
		t.Fatalf("system prompt has no %q section", heading)
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return heading + "\n" + section + "\n"
}
