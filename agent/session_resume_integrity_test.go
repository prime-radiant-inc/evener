package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestRestoreSessionRejectsCorruptTranscriptWithoutMutation(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	stateDir := t.TempDir()
	meta := resumeIntegrityMeta(sessionID)
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	body := `{"kind":"header","format_version":2,"session_id":"` + sessionID + `"}` + "\n{malformed}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile transcript: %v", err)
	}
	before := snapshotResumeIntegrityTree(t, stateDir)

	sess, err := restoreIntegritySession(stateDir, meta)
	if sess != nil {
		sess.Close()
	}
	if err == nil {
		t.Fatal("RestoreSessionFromMeta accepted a corrupt complete transcript line")
	}
	if after := snapshotResumeIntegrityTree(t, stateDir); after != before {
		t.Fatalf("failed resume mutated artifacts:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRestoreSessionRejectsTranscriptSessionMismatchWithoutMutation(t *testing.T) {
	const requestedID = "02wMz5Txv1C3Hut0M8GCeB"
	stateDir := t.TempDir()
	meta := resumeIntegrityMeta(requestedID)
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	path := filepath.Join(stateDir, sessionsSubdir, requestedID+".transcript.jsonl")
	body := `{"kind":"header","format_version":2,"session_id":"02wMz5Txv2enqVTitaig6F"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile transcript: %v", err)
	}
	before := snapshotResumeIntegrityTree(t, stateDir)

	sess, err := restoreIntegritySession(stateDir, meta)
	if sess != nil {
		sess.Close()
	}
	if err == nil {
		t.Fatal("RestoreSessionFromMeta accepted another session's transcript")
	}
	if after := snapshotResumeIntegrityTree(t, stateDir); after != before {
		t.Fatalf("failed resume mutated artifacts:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRestoreSessionRejectsSymlinkTranscriptWithoutMutation(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	stateDir := t.TempDir()
	meta := resumeIntegrityMeta(sessionID)
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.jsonl")
	body := `{"kind":"header","format_version":2,"session_id":"` + sessionID + `"}` + "\npartial-tail"
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	before := snapshotResumeIntegrityTree(t, stateDir)

	sess, err := restoreIntegritySession(stateDir, meta)
	if sess != nil {
		sess.Close()
	}
	if err == nil {
		t.Fatal("RestoreSessionFromMeta followed a transcript symlink")
	}
	if after := snapshotResumeIntegrityTree(t, stateDir); after != before {
		t.Fatalf("failed resume mutated artifacts:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile target: %v", readErr)
	}
	if string(got) != body {
		t.Fatalf("symlink target changed: got %q want %q", got, body)
	}
}

func resumeIntegrityMeta(sessionID string) schema.SessionMeta {
	return schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
	}
}

func restoreIntegritySession(stateDir string, meta schema.SessionMeta) (*Session, error) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	return RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile(meta.Model),
		execenv.NewLocalExecutionEnvironment(stateDir),
		meta,
		RestoreSessionConfig{StateDir: stateDir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}},
	)
}

func snapshotResumeIntegrityTree(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		snapshot.WriteString(rel)
		snapshot.WriteByte(' ')
		snapshot.WriteString(info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot.WriteString(" -> ")
			snapshot.WriteString(target)
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.WriteString(" = ")
			snapshot.Write(contents)
		}
		snapshot.WriteByte('\n')
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot artifacts: %v", err)
	}
	return snapshot.String()
}
