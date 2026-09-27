package main

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// writeReviewRun writes one run under base the way a run lays it out: a
// state directory whose root transcript mentions a file in the work
// directory, and result.json. It returns the work and state directories.
func writeReviewRun(t *testing.T, base string) (workDir, stateDir string) {
	t.Helper()
	workDir, stateDir = filepath.Join(base, "work"), filepath.Join(base, "state")
	writeFluencyMeta(t, stateDir, proseRootID, "", time.Now())
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(
			textPart("I read "+workDir+"/tally/sum.go and found the loop."),
			fluencyToolCall("read_file", `{"file_path":"`+workDir+`/tally/sum.go"}`),
		),
	})
	res := probeResult{Probe: "prose.bugfix-tally", Model: "lunarouter/m", Repetition: 1, WorkDir: workDir, StateDir: stateDir}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "result.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return workDir, stateDir
}

// TestWriteReviewPackMasksRunPaths: run paths contain the version label, and
// the transcript render truncates argument previews, which can cut a path in
// the middle of the label. So a packet carries no path under the mask root
// and no label, and only the key maps the packet back to its run.
func TestWriteReviewPackMasksRunPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workDir, stateDir := writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m", "prose.bugfix-tally", "rep-01"))

	packets := filepath.Join(t.TempDir(), "packets")
	key, err := writeReviewPack([]labeledDir{{Label: "v1-A", Dir: filepath.Join(root, "v1-A")}}, packets, root, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatalf("writeReviewPack: %v", err)
	}
	if len(key) != 1 || key[0].Label != "v1-A" || key[0].Probe != "prose.bugfix-tally" || key[0].Model != "lunarouter/m" {
		t.Fatalf("key = %+v, want one entry for v1-A", key)
	}
	packet, err := os.ReadFile(filepath.Join(packets, key[0].Packet))
	if err != nil {
		t.Fatal(err)
	}
	text := string(packet)
	for _, leak := range []string{"v1-A", workDir, stateDir, root} {
		if strings.Contains(text, leak) {
			t.Errorf("packet contains %q:\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "found the loop") {
		t.Errorf("packet lost the transcript text:\n%s", text)
	}
}

func TestPathWithin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "key.json"), true},
		{filepath.Join(dir, "sub", "key.json"), true},
		{filepath.Join(filepath.Dir(dir), "key.json"), false},
	} {
		got, err := pathWithin(dir, c.path)
		if err != nil || got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, %v; want %v", dir, c.path, got, err, c.want)
		}
	}
}

// TestReviewPackRefusesLayoutsThatBreakTheBlinding: a key inside the packets
// directory would be in front of the reviewer, and results outside the mask
// root would keep their labeled paths.
func TestReviewPackRefusesLayoutsThatBreakTheBlinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	results := "v1-A=" + filepath.Join(root, "v1-A")
	packets := filepath.Join(t.TempDir(), "packets")
	outside := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"review-pack", "--results", results, "--packets", packets, "--key", filepath.Join(outside, "key.json")}, "are required"},
		{[]string{"review-pack", "--results", results, "--mask-root", root, "--packets", packets, "--key", filepath.Join(packets, "key.json")}, "outside --packets"},
		{[]string{"review-pack", "--results", "v1-A=" + outside, "--mask-root", root, "--packets", packets, "--key", filepath.Join(root, "key.json")}, "outside --mask-root"},
	} {
		if err := run(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("run(%q) = %v, want an error containing %q", c.args, err, c.want)
		}
	}
}

func TestReviewPackWritesPacketsAndKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m", "prose.bugfix-tally", "rep-01"))
	packets := filepath.Join(t.TempDir(), "packets")
	keyPath := filepath.Join(t.TempDir(), "key.json")
	err := run([]string{"review-pack", "--results", "v1-A=" + filepath.Join(root, "v1-A"), "--mask-root", root,
		"--packets", packets, "--key", keyPath, "--seed", "7"})
	if err != nil {
		t.Fatalf("review-pack: %v", err)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var key []reviewEntry
	if err := json.Unmarshal(data, &key); err != nil {
		t.Fatalf("key file %q: %v", data, err)
	}
	if len(key) != 1 || key[0].Label != "v1-A" {
		t.Fatalf("key = %+v, want one entry for v1-A", key)
	}
	if _, err := os.Stat(filepath.Join(packets, key[0].Packet)); err != nil {
		t.Errorf("packet named in the key: %v", err)
	}
}
