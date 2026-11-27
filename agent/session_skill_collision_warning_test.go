package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

const collisionSkillBody = "---\nname: dup\ndescription: fixture\n---\nBODY\n"

// newCollisionSession builds a session whose project skill "dup" shadows the
// user-config skill of the same name. It returns the catalog winner and the
// displaced source.
func newCollisionSession(t *testing.T) (*Session, string, string) {
	return newCollisionSessionWithWinnerBody(t, collisionSkillBody)
}

func newCollisionSessionWithWinnerBody(t *testing.T, winnerBody string) (*Session, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := skillFixtureRoot(t)
	loserRoot := filepath.Join(xdg, "evener")
	writeSkillMD(t, loserRoot, "dup", collisionSkillBody)
	writeSkillMD(t, root, "dup", winnerBody)
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

func warningsForPaths(warnings []events.WarningData, paths ...string) []events.WarningData {
	var out []events.WarningData
	for _, w := range warnings {
		if w.Code != events.WarningCodeSkillDiagnostic {
			continue
		}
		for _, path := range paths {
			if strings.Contains(w.Message, path) {
				out = append(out, w)
				break
			}
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

func TestSkillCollision_SummarizesEveryShadowedSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := skillFixtureRoot(t)
	extra := t.TempDir()
	loserRoot := filepath.Join(xdg, "evener")
	writeSkillMD(t, loserRoot, "dup", collisionSkillBody)
	writeSkillMD(t, root, "dup", collisionSkillBody)
	writeSkillMD(t, extra, "dup", collisionSkillBody)
	extraSkills := filepath.Join(extra, "skills")
	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{SkillsDirs: []string{extraSkills}, testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(extraSkills, "dup", "SKILL.md")
	userLoser := filepath.Join(loserRoot, "skills", "dup", "SKILL.md")
	projectLoser := filepath.Join(root, "skills", "dup", "SKILL.md")
	if _, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{
		Name:         "dup",
		Route:        "user_slash",
		InvocationID: "inv-1",
	}}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got := warningsForPaths(drainWarnings(t, sess), winner, userLoser, projectLoser)
	if len(got) != 1 {
		t.Fatalf("three-way collision emitted %d warnings, want one summary: %+v", len(got), got)
	}
	for _, path := range []string{winner, userLoser, projectLoser} {
		if !strings.Contains(got[0].Message, path) || !strings.Contains(got[0].Hint, path) {
			t.Fatalf("summary does not name %q in both fields: %+v", path, got[0])
		}
	}
}

func TestSkillCollision_WarnsWhenControlsDenyTheSelectedCopy(t *testing.T) {
	sess, winner, loser := newCollisionSessionWithWinnerBody(t, "---\nname: dup\ndescription: fixture\nuser-invocable: false\n---\nBODY\n")
	_, err := sess.prepareSkillActivations(context.Background(), []skillInvocation{{
		Name:         "dup",
		Route:        "user_slash",
		InvocationID: "inv-1",
	}})
	var activationErr *skillActivationError
	if !errors.As(err, &activationErr) || activationErr.Code != "policy_denied" {
		t.Fatalf("prepare err = %v, want policy_denied", err)
	}
	got := warningsForSource(drainWarnings(t, sess), winner)
	if len(got) != 1 {
		t.Fatalf("denied activation emitted %d collision warnings, want one: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, loser) || !strings.Contains(got[0].Hint, loser) {
		t.Fatalf("collision warning does not name the shadowed user copy %q: %+v", loser, got[0])
	}
}
