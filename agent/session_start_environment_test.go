package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestSessionStartCarriesInstalledEnvironment(t *testing.T) {
	sess := newSession(t, withStateDir(t.TempDir()), withoutGitSnapshot())
	assertStart := func(s *Session, restored bool) {
		t.Helper()
		for {
			select {
			case event := <-s.Events():
				if start, ok := event.Data.(events.SessionStartData); ok {
					if start.WorkingDir != s.currentEnv().WorkingDirectory() || start.WorkingDir == "" || start.Restored != restored {
						t.Fatalf("start %+v differs from installed environment %q", start, s.currentEnv().WorkingDirectory())
					}
					return
				}
			default:
				t.Fatal("missing session start")
			}
		}
	}
	assertStart(sess, false)
	meta := sess.Meta()
	stateDir := sess.StateDir()
	sess.Close()
	// Restore into a different actual environment. Persisted launch metadata
	// must not supply the seed, even when the identity belongs to a child.
	meta.IsSubagent = true
	installed := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(installed), meta,
		RestoreSessionConfig{StateDir: stateDir, testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.currentEnv().WorkingDirectory() != installed || installed == meta.EnvInfo.WorkingDir {
		t.Fatal("restore fixture did not install a different actual environment")
	}
	assertStart(restored, true)
}
