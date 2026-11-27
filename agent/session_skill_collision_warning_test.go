package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// newCollisionSession builds a session whose project skill "dup" shadows the
// user-config skill of the same name. It returns the catalog winner and the
// displaced source.
func newCollisionSession(t *testing.T) (*Session, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := skillFixtureRoot(t)
	loserRoot := filepath.Join(xdg, "evener")
	const body = "---\nname: dup\ndescription: fixture\n---\nBODY\n"
	writeSkillMD(t, loserRoot, "dup", body)
	writeSkillMD(t, root, "dup", body)
	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatal(err)
	}
	return sess, filepath.Join(root, "skills", "dup", "SKILL.md"), filepath.Join(loserRoot, "skills", "dup", "SKILL.md")
}

func warningsForSource(warnings []events.WarningData, source string) []events.WarningData {
	var out []events.WarningData
	for _, w := range warnings {
		if w.Code == events.WarningCodeSkillDiagnostic && strings.Contains(w.Message, source) {
			out = append(out, w)
		}
	}
	return out
}

func TestSkillCollision_StartupIsQuiet(t *testing.T) {
	sess, winner, loser := newCollisionSession(t)
	status := sess.DetailedStatus()
	found := false
	for _, d := range status.SkillDiagnostics {
		if d.Category == "collision" && d.Name == "dup" && d.Source == winner && d.OtherSource == loser {
			found = true
		}
	}
	if !found {
		t.Fatalf("collision diagnostic missing from status: %+v", status.SkillDiagnostics)
	}
	if got := warningsForSource(drainWarnings(t, sess), winner); len(got) != 0 {
		t.Fatalf("session start emitted %d collision warnings, want none: %+v", len(got), got)
	}
}

func TestSkillCollision_WarnsOnceOnGenuineActivation(t *testing.T) {
	for _, route := range []string{"user_slash", "user_selection", "model_tool"} {
		t.Run(route, func(t *testing.T) {
			sess, winner, loser := newCollisionSession(t)
			for _, invocationID := range []string{"inv-1", "inv-2"} {
				batch, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{
					Name:         "dup",
					Route:        route,
					InvocationID: invocationID,
				}})
				if err != nil || batch == nil || len(batch.Items) != 1 {
					t.Fatalf("prepare %s: batch=%+v err=%v", invocationID, batch, err)
				}
			}
			got := warningsForSource(drainWarnings(t, sess), winner)
			if len(got) != 1 {
				t.Fatalf("activation route %s emitted %d collision warnings, want exactly 1: %+v", route, len(got), got)
			}
			if !strings.Contains(got[0].Message, loser) {
				t.Fatalf("collision warning does not name the displaced source %q: %+v", loser, got[0])
			}
		})
	}
}

func TestSkillCollision_BackgroundReloadDoesNotWarn(t *testing.T) {
	sess, winner, _ := newCollisionSession(t)
	batch, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{
		Name:         "dup",
		Route:        "role_preload",
		InvocationID: "inv-1",
	}})
	if err != nil || batch == nil || len(batch.Items) != 1 {
		t.Fatalf("role preload: batch=%+v err=%v", batch, err)
	}
	if got := warningsForSource(drainWarnings(t, sess), winner); len(got) != 0 {
		t.Fatalf("background preload emitted %d collision warnings, want none: %+v", len(got), got)
	}
}
