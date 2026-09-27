package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// reviewRunReport is the final message writeReviewRun's agent sends the user,
// long enough that a preview would cut it.
const reviewRunReport = "Fixed the weekly totals. tally.Sum started its loop at index 1, so it skipped the first value; it now adds every value, and go test passes."

// writeReviewRun writes repetition rep of prose.bugfix-tally into cellDir the
// way a run lays it out: <cellDir>/prose.bugfix-tally/rep-NN holds a state
// directory whose root transcript mentions a file in the work directory, and
// the result file. It returns the work and state directories.
// The transcript opens with the harness's environment turn, names the version
// label outside any path, as an agent that lists the directories above its
// work directory would, and ends with reviewRunReport.
func writeReviewRun(t *testing.T, cellDir string, rep int) (workDir, stateDir string) {
	t.Helper()
	base := filepath.Join(cellDir, "prose.bugfix-tally", fmt.Sprintf("rep-%02d", rep))
	workDir, stateDir = filepath.Join(base, "work"), filepath.Join(base, "state")
	rootMeta(t, stateDir, proseRootID)
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		schema.NewTurn(schema.TurnEnvironment, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{textPart("date: 2026-09-27 14:00 PDT")}}),
		assistantTurn(
			textPart("I read "+workDir+"/tally/sum.go and found the loop."),
			fluencyToolCall("read_file", `{"file_path":"`+workDir+`/tally/sum.go"}`),
		),
		assistantTurn(textPart("The directory four levels up lists v1-A.")),
		assistantTurn(fluencyToolCall("communicate", `{"message":"`+reviewRunReport+`","end_turn":true}`)),
	})
	res := probeResult{Probe: "prose.bugfix-tally", Model: "lunarouter/m", Repetition: rep, WorkDir: workDir, StateDir: stateDir}
	if err := writeProbeResult(cellDir, res); err != nil {
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
	workDir, stateDir := writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m"), 1)

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

// TestWriteReviewPackShowsWhatTheUserSawAndNoHarnessChrome: a packet carries
// every message to the user whole, since those are what the reader scores. It
// leaves out harness chrome that would tell the reader which runs go together,
// such as the environment turn's date and the session id. Every packet gets
// the same modification time, so listing packets by time says nothing about
// versions.
func TestWriteReviewPackShowsWhatTheUserSawAndNoHarnessChrome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for rep := 1; rep <= 2; rep++ {
		writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m"), rep)
	}
	packets := filepath.Join(t.TempDir(), "packets")
	key, err := writeReviewPack([]labeledDir{{Label: "v1-A", Dir: filepath.Join(root, "v1-A")}}, packets, root, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatalf("writeReviewPack: %v", err)
	}
	if len(key) != 2 {
		t.Fatalf("key = %+v, want two packets", key)
	}
	var modTimes []time.Time
	for _, entry := range key {
		path := filepath.Join(packets, entry.Packet)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, reviewRunReport) {
			t.Errorf("packet cut the message to the user:\n%s", text)
		}
		for _, chrome := range []string{"2026-09-27 14:00", proseRootID} {
			if strings.Contains(text, chrome) {
				t.Errorf("packet carries harness chrome %q:\n%s", chrome, text)
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		modTimes = append(modTimes, info.ModTime())
	}
	if !modTimes[0].Equal(modTimes[1]) {
		t.Errorf("packets were modified at %v, want one shared time", modTimes)
	}
}

// TestReviewPackRefusesALabelWithoutADigit: review-pack masks the label
// wherever it appears, so an ordinary word such as "baseline" would be masked
// in the agents' own writing, and only in that version's packets.
func TestReviewPackRefusesALabelWithoutADigit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeReviewRun(t, filepath.Join(root, "baseline", "lunarouter-m"), 1)
	err := run([]string{"review-pack", "--results", "baseline=" + filepath.Join(root, "baseline"), "--mask-root", root,
		"--packets", filepath.Join(t.TempDir(), "packets"), "--key", filepath.Join(t.TempDir(), "key.json")})
	if err == nil || !strings.Contains(err.Error(), "digit") {
		t.Fatalf("review-pack = %v, want a refusal asking for a digit in the label", err)
	}
}

// TestReviewPackMasksUnderARelativeMaskRoot: a relative --mask-root still
// masks the absolute paths a transcript holds. Not parallel: it changes the
// working directory.
func TestReviewPackMasksUnderARelativeMaskRoot(t *testing.T) {
	root := t.TempDir()
	writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m"), 1)
	t.Chdir(root)
	packets := filepath.Join(t.TempDir(), "packets")
	err := run([]string{"review-pack", "--results", "v1-A=v1-A", "--mask-root", ".",
		"--packets", packets, "--key", filepath.Join(t.TempDir(), "key.json"), "--seed", "7"})
	if err != nil {
		t.Fatalf("review-pack: %v", err)
	}
	entries, err := os.ReadDir(packets)
	if err != nil || len(entries) != 1 {
		t.Fatalf("packets = %v, %v; want one", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(packets, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) {
		t.Errorf("packet contains the run root %q:\n%s", root, data)
	}
}

// TestMaskRunDetailsMasksTheResolvedRoot: a tool that resolves symlinks
// prints a run path under the root's real location, which is masked as well.
func TestMaskRunDetailsMasksTheResolvedRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	text := "opened " + filepath.Join(resolved, "v1-A", "work", "sum.go") + " and " + filepath.Join(link, "v1-A", "work", "sum.go")
	if got, want := maskRunDetails(text, link, "v1-A"), "opened <run> and <run>"; got != want {
		t.Errorf("maskRunDetails = %q, want %q", got, want)
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
	writeReviewRun(t, filepath.Join(root, "v1-A", "lunarouter-m"), 1)
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
