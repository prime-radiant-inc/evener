package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// stampFixture is a projects root with one project and one session, and a way
// to read the digest.
type stampFixture struct {
	project string
	glob    string
}

func newStampFixture(t *testing.T) *stampFixture {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(schema.SessionsDir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	return &stampFixture{project: project, glob: filepath.Join(root, "projects", "*")}
}

func (f *stampFixture) stamp(t *testing.T) uint64 {
	t.Helper()
	stamp, err := SessionTreeStamp(f.glob)
	if err != nil {
		t.Fatalf("SessionTreeStamp: %v", err)
	}
	return stamp
}

const (
	stampSessionID = "02wMz5Txv1C3Hut0M8GCeB"
	stampOtherID   = "02wMz5Txv1C3Hut0M8GCeC"
)

// TestSessionTreeStampIsStableWhenNothingChanges pins that an idle tree digests
// to the same value twice. Without this the gate would run the full pass on
// every tick and the change would buy nothing.
func TestSessionTreeStampIsStableWhenNothingChanges(t *testing.T) {
	f := newStampFixture(t)
	writeMeta(t, f.project, schema.SessionMeta{ID: stampSessionID, Name: "one", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	first := f.stamp(t)
	second := f.stamp(t)
	if first != second {
		t.Fatalf("an idle tree digested differently: %d then %d", first, second)
	}
}

// TestSessionTreeStampSeesEverySweptWrite is the gate's safety property: every
// write the gated rebuild reads must move the digest, or the gate would skip a
// rebuild over content that changed and a client would wait a whole extra
// interval (or forever) to see it. Each case is a real writer's effect on disk.
func TestSessionTreeStampSeesEverySweptWrite(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, project string)
	}{
		{
			name: "a session meta is saved",
			write: func(t *testing.T, project string) {
				writeMeta(t, project, schema.SessionMeta{ID: stampOtherID, Name: "new", UpdatedAt: time.Unix(1_700_000_100, 0).UTC()})
			},
		},
		{
			name: "an indexed session meta is rewritten",
			write: func(t *testing.T, project string) {
				writeMeta(t, project, schema.SessionMeta{ID: stampSessionID, Name: "renamed", UpdatedAt: time.Unix(1_700_000_200, 0).UTC()})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStampFixture(t)
			writeMeta(t, f.project, schema.SessionMeta{ID: stampSessionID, Name: "one", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
			writeStampTranscript(t, f.project, stampSessionID, "the first message")
			before := f.stamp(t)

			tc.write(t, f.project)
			if after := f.stamp(t); after == before {
				t.Fatalf("%s did not move the digest, so the gate would skip a changed tree", tc.name)
			}
		})
	}
}

// TestSessionTreeStampIgnoresArtifactsTheRebuildDoesNotRead pins the other side:
// what the rebuild never opens must not move the digest, or the gate would fire
// on every turn and buy nothing again. A transcript is the important one — it
// grows on every turn, and the rebuild reads only the metas.
func TestSessionTreeStampIgnoresArtifactsTheRebuildDoesNotRead(t *testing.T) {
	f := newStampFixture(t)
	writeMeta(t, f.project, schema.SessionMeta{ID: stampSessionID, Name: "one", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()})
	writeStampTranscript(t, f.project, stampSessionID, "the first message")
	before := f.stamp(t)

	// The writer that matters: a turn appends to the transcript.
	appendStampTranscript(t, f.project, stampSessionID, "a later message")
	writeStampTranscript(t, f.project, stampOtherID, "a brand new session's first message")
	if after := f.stamp(t); after != before {
		t.Fatal("a transcript write moved the digest, so every turn would run a rebuild that finds nothing")
	}

	sessionsDir := schema.SessionsDir(f.project)
	for name, body := range map[string]string{
		stampSessionID + ".api.jsonl":                           "{}\n",
		stampSessionID + ".log.jsonl":                           "{}\n",
		stampSessionID + ".meta.json.lock":                      "",
		stampSessionID + ".boot-generation":                     "7",
		stampSessionID + ".transcript.jsonl.index-abcd":         "sidecar",
		stampSessionID + ".transcript.jsonl.appwire-index.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if after := f.stamp(t); after != before {
		t.Fatal("an artifact the rebuild does not read moved the digest, so the gate would fire on every turn")
	}
}

func writeStampTranscript(t *testing.T, project, sessionID, message string) {
	t.Helper()
	path := schema.SessionTranscriptPath(project, sessionID)
	body := `{"kind":"header","format_version":2,"session_id":"` + sessionID + `"}` + "\n" +
		`{"kind":"entry","seq":1,"turn":{"kind":"user_input","message":{"role":"user","content":[{"kind":"text","text":"` + message + `"}]}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendStampTranscript(t *testing.T, project, sessionID, message string) {
	t.Helper()
	f, err := os.OpenFile(schema.SessionTranscriptPath(project, sessionID), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	line := `{"kind":"entry","seq":2,"turn":{"kind":"user_input","message":{"role":"user","content":[{"kind":"text","text":"` + message + `"}]}}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
