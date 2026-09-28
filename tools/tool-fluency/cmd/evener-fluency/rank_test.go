package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGroupForRankingGroupsByModelTaskRepetitionAndSkipsNamedTasks(t *testing.T) {
	t.Parallel()
	entries := []reviewEntry{
		{Packet: "a.md", Label: "v0", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
		{Packet: "b.md", Label: "v1", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
		{Packet: "c.md", Label: "v0", Model: "m1", Probe: "prose.smoke", Repetition: 1},
	}
	groups, err := groupForRanking(entries, map[string]bool{"prose.smoke": true})
	if err != nil {
		t.Fatalf("groupForRanking: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want one group (prose.smoke skipped)", groups)
	}
	k := rankGroupKey{Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1}
	if len(groups[k]) != 2 {
		t.Fatalf("group = %+v, want 2 entries", groups[k])
	}
}

// TestGroupForRankingRefusesTwoPacketsForOneLabel: this can happen once
// review-pack skips blocked runs (a blocked run and its retry share label,
// model, task, and repetition) only if both copies end up passed or failed,
// which is unusual, so the error must name the two colliding packets and
// their result.json paths clearly, not just the shape of the collision.
func TestGroupForRankingRefusesTwoPacketsForOneLabel(t *testing.T) {
	t.Parallel()
	entries := []reviewEntry{
		{Packet: "a.md", Label: "v0", Model: "m1", Probe: "p", Repetition: 1, Result: "results/a/result.json"},
		{Packet: "b.md", Label: "v0", Model: "m1", Probe: "p", Repetition: 1, Result: "results/b/result.json"},
	}
	_, err := groupForRanking(entries, nil)
	if err == nil || !strings.Contains(err.Error(), "two packets") {
		t.Fatalf("groupForRanking = %v, want a refusal naming two packets", err)
	}
	for _, want := range []string{"a.md", "results/a/result.json", "b.md", "results/b/result.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("groupForRanking error %q does not name %q", err, want)
		}
	}
}

func TestAssignLettersIsAPermutationReproducibleBySeed(t *testing.T) {
	t.Parallel()
	entries := []reviewEntry{{Label: "v0"}, {Label: "v1"}, {Label: "v2"}}
	a, err := assignLetters(entries, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := assignLetters(entries, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(a, b, func(x, y reviewEntry) bool { return x.Label == y.Label }) {
		t.Errorf("same seed gave different orders: %v vs %v", a, b)
	}
	labels := map[string]bool{}
	for _, e := range a {
		labels[e.Label] = true
	}
	if len(labels) != 3 {
		t.Errorf("assignLetters lost or duplicated a label: %v", a)
	}
}

func TestAssignLettersRefusesMoreThan26(t *testing.T) {
	t.Parallel()
	entries := make([]reviewEntry, 27)
	for i := range entries {
		entries[i] = reviewEntry{Label: fmt.Sprintf("v%d", i)}
	}
	_, err := assignLetters(entries, rand.New(rand.NewPCG(1, 1)))
	if err == nil || !strings.Contains(err.Error(), "26") {
		t.Fatalf("assignLetters = %v, want a refusal about the letter limit", err)
	}
}

func TestBuildRankSetsOrdersGroupsAndSkipsSingleVersionGroups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.md"), "# Task prose.bugfix-tally\n\npacket v0\n")
	mustWrite(t, filepath.Join(dir, "b.md"), "# Task prose.bugfix-tally\n\npacket v1\n")
	mustWrite(t, filepath.Join(dir, "c.md"), "# Task prose.smoke\n\npacket v0\n")
	mustWrite(t, filepath.Join(dir, "d.md"), "# Task prose.only-one\n\npacket v0\n")
	entries := []reviewEntry{
		{Packet: "a.md", Label: "v0", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
		{Packet: "b.md", Label: "v1", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
		{Packet: "c.md", Label: "v0", Model: "m1", Probe: "prose.smoke", Repetition: 1},
		{Packet: "d.md", Label: "v0", Model: "m1", Probe: "prose.only-one", Repetition: 1},
	}
	sets, key, notes, err := buildRankSets(entries, dir, map[string]bool{"prose.smoke": true}, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatalf("buildRankSets: %v", err)
	}
	if len(sets) != 1 {
		t.Fatalf("sets = %+v, want one set (prose.smoke skipped, prose.only-one has one version)", sets)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "prose.only-one") {
		t.Errorf("notes = %v, want a note about prose.only-one", notes)
	}
	set := sets[0]
	if set.Number != 1 || set.Model != "m1" || set.Probe != "prose.bugfix-tally" {
		t.Errorf("set = %+v", set)
	}
	if len(set.Packets) != 2 {
		t.Fatalf("set.Packets = %+v, want 2", set.Packets)
	}
	gotLabels := map[string]string{}
	for _, p := range set.Packets {
		gotLabels[p.ID] = p.Entry.Label
		if !strings.Contains(p.Content, "packet "+p.Entry.Label) {
			t.Errorf("packet %s content = %q, want it to hold %s's own text", p.ID, p.Content, p.Entry.Label)
		}
	}
	if len(key) != 2 {
		t.Fatalf("key = %+v, want 2 entries", key)
	}
	for _, k := range key {
		if k.Set != 1 {
			t.Errorf("key entry %+v has the wrong set number", k)
		}
		if gotLabels[k.Packet] != k.Label {
			t.Errorf("key entry %+v does not match the built set", k)
		}
	}
}

func TestRenderRankSetsIncludesPromptSetHeaderAndPackets(t *testing.T) {
	t.Parallel()
	sets := []rankSet{{Number: 3, Model: "m1", Probe: "prose.bugfix-tally", Packets: []rankSetPacket{
		{ID: "A", Content: "first packet body"},
		{ID: "B", Content: "second packet body"},
	}}}
	text := renderRankSets("Judge like an editor.", sets)
	for _, want := range []string{"Judge like an editor.", "Set 3", "m1", "prose.bugfix-tally", "Packet A", "first packet body", "Packet B", "second packet body", `"ranking"`} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered sets missing %q:\n%s", want, text)
		}
	}
}

func TestRunRankSetsWritesOutAndKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	packets := filepath.Join(dir, "packets")
	mustWrite(t, filepath.Join(packets, "a.md"), "# Task prose.bugfix-tally\n\npacket v0\n")
	mustWrite(t, filepath.Join(packets, "b.md"), "# Task prose.bugfix-tally\n\npacket v1\n")
	keyIn := filepath.Join(dir, "review-pack-key.json")
	entries := []reviewEntry{
		{Packet: "a.md", Label: "v0", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
		{Packet: "b.md", Label: "v1", Model: "m1", Probe: "prose.bugfix-tally", Repetition: 1},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, keyIn, string(data))
	promptPath := filepath.Join(dir, "prompt.md")
	mustWrite(t, promptPath, "Judge writing quality.")
	out := filepath.Join(dir, "sets.txt")
	keyOut := filepath.Join(dir, "sets-key.json")
	err = run([]string{"rank-sets", "--review-pack-key", keyIn, "--packets", packets, "--out", out, "--key", keyOut, "--prompt", promptPath, "--seed", "7"})
	if err != nil {
		t.Fatalf("rank-sets: %v", err)
	}
	text := mustRead(t, out)
	if !strings.Contains(text, "Judge writing quality.") || !strings.Contains(text, "Set 1") {
		t.Errorf("sets file = %q", text)
	}
	key := mustReadRankKey(t, keyOut)
	if len(key) != 2 {
		t.Fatalf("key = %+v, want 2 entries", key)
	}
}

func TestRunRankSetsRefusesWhenNothingIsLeftToRank(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	packets := filepath.Join(dir, "packets")
	mustWrite(t, filepath.Join(packets, "a.md"), "packet\n")
	mustWrite(t, filepath.Join(packets, "b.md"), "packet\n")
	keyIn := filepath.Join(dir, "key.json")
	entries := []reviewEntry{
		{Packet: "a.md", Label: "v0", Model: "m1", Probe: "prose.smoke", Repetition: 1},
		{Packet: "b.md", Label: "v1", Model: "m1", Probe: "prose.smoke", Repetition: 1},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, keyIn, string(data))
	promptPath := filepath.Join(dir, "prompt.md")
	mustWrite(t, promptPath, "prompt")
	err = run([]string{"rank-sets", "--review-pack-key", keyIn, "--packets", packets, "--out", filepath.Join(dir, "out.txt"), "--key", filepath.Join(dir, "key-out.json"), "--prompt", promptPath})
	if err == nil || !strings.Contains(err.Error(), "no ranking sets") {
		t.Fatalf("rank-sets = %v, want a refusal", err)
	}
}

func TestRunRankSetsRequiresItsFlags(t *testing.T) {
	t.Parallel()
	if err := run([]string{"rank-sets"}); err == nil || !strings.Contains(err.Error(), "are required") {
		t.Fatalf("rank-sets = %v, want a refusal naming its required flags", err)
	}
}

func TestScoreReviewsAggregatesMeanFirstLast(t *testing.T) {
	t.Parallel()
	key := []rankKeyEntry{
		{Set: 1, Packet: "A", Label: "v0", Model: "m1", Probe: "p"},
		{Set: 1, Packet: "B", Label: "v1", Model: "m1", Probe: "p"},
		{Set: 2, Packet: "A", Label: "v1", Model: "m1", Probe: "q"},
		{Set: 2, Packet: "B", Label: "v0", Model: "m1", Probe: "q"},
	}
	reviews := []reviewLine{
		{Set: 1, Ranking: []string{"B", "A"}, Writing: map[string]int{"A": 3, "B": 5}, Why: "A rambles"},
		{Set: 2, Ranking: []string{"A", "B"}, Writing: map[string]int{"A": 4, "B": 2}, Why: "B buries the outcome"},
	}
	rows, err := scoreReviews(key, reviews)
	if err != nil {
		t.Fatalf("scoreReviews: %v", err)
	}
	got := map[string]rankScoreRow{}
	for _, r := range rows {
		got[r.Label] = r
	}
	// Set 1: ranking B,A -> B first (v1), A last (v0). writing A=3,B=5.
	// Set 2: ranking A,B -> A first (v1), B last (v0). writing A=4,B=2.
	v0 := got["v0"]
	if v0.N != 2 || v0.Sum != 3+2 || v0.FirstCount != 0 || v0.LastCount != 2 {
		t.Errorf("v0 row = %+v", v0)
	}
	v1 := got["v1"]
	if v1.N != 2 || v1.Sum != 5+4 || v1.FirstCount != 2 || v1.LastCount != 0 {
		t.Errorf("v1 row = %+v", v1)
	}
}

func TestScoreReviewsValidatesRankingsAgainstTheKey(t *testing.T) {
	t.Parallel()
	key := []rankKeyEntry{
		{Set: 1, Packet: "A", Label: "v0", Model: "m1", Probe: "p"},
		{Set: 1, Packet: "B", Label: "v1", Model: "m1", Probe: "p"},
	}
	for _, c := range []struct {
		name string
		line reviewLine
		want string
	}{
		{"unknown set", reviewLine{Set: 9, Ranking: []string{"A", "B"}, Writing: map[string]int{"A": 3, "B": 3}}, "not in the key"},
		{"wrong length", reviewLine{Set: 1, Ranking: []string{"A"}, Writing: map[string]int{"A": 3}}, "want 2"},
		{"duplicate id", reviewLine{Set: 1, Ranking: []string{"A", "A"}, Writing: map[string]int{"A": 3, "B": 3}}, "ranked twice"},
		{"unknown ranking id", reviewLine{Set: 1, Ranking: []string{"A", "C"}, Writing: map[string]int{"A": 3, "C": 3}}, "unknown packet"},
		{"unknown writing id", reviewLine{Set: 1, Ranking: []string{"A", "B"}, Writing: map[string]int{"A": 3, "C": 3}}, "unknown packet"},
		{"missing writing id", reviewLine{Set: 1, Ranking: []string{"A", "B"}, Writing: map[string]int{"A": 3}}, "missing packet"},
		{"score out of range", reviewLine{Set: 1, Ranking: []string{"A", "B"}, Writing: map[string]int{"A": 3, "B": 9}}, "want 1-5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := scoreReviews(key, []reviewLine{c.line})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("scoreReviews(%+v) = %v, want error containing %q", c.line, err, c.want)
			}
		})
	}
}

func TestReadReviewLinesParsesJSONLinesAndSkipsBlank(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.jsonl")
	mustWrite(t, path, `{"set":1,"ranking":["A","B"],"writing":{"A":3,"B":4},"why":"ok"}

{"set":2,"ranking":["B","A"],"writing":{"A":2,"B":5},"why":"fine"}
`)
	lines, err := readReviewLines(path)
	if err != nil {
		t.Fatalf("readReviewLines: %v", err)
	}
	if len(lines) != 2 || lines[0].Set != 1 || lines[1].Why != "fine" {
		t.Errorf("lines = %+v", lines)
	}
}

func TestReadReviewLinesReportsWhichLineFailedToParse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.jsonl")
	mustWrite(t, path, "{\"set\":1,\"ranking\":[],\"writing\":{}}\nnot json\n")
	_, err := readReviewLines(path)
	if err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("readReviewLines = %v, want an error naming line 2", err)
	}
}

// TestReadReviewLinesRefusesARepeatedSetWithinOneFile: scoreReviews
// aggregates across every reviewLine it sees, so a set appearing twice in
// one reviewer's file would silently double-count that reviewer's judgment.
// Two different reviewers' files may each cover the same set (that is the
// point of --reviews being repeatable); only a repeat within ONE file is an
// error, so this is checked per file, before runRankScore merges them.
func TestReadReviewLinesRefusesARepeatedSetWithinOneFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.jsonl")
	mustWrite(t, path, `{"set":1,"ranking":["A","B"],"writing":{"A":3,"B":4},"why":"ok"}
{"set":2,"ranking":["A","B"],"writing":{"A":2,"B":5},"why":"fine"}
{"set":1,"ranking":["B","A"],"writing":{"A":1,"B":2},"why":"again"}
`)
	_, err := readReviewLines(path)
	if err == nil || !strings.Contains(err.Error(), "set 1") || !strings.Contains(err.Error(), path) {
		t.Fatalf("readReviewLines = %v, want a refusal naming set 1 and %s", err, path)
	}
}

func TestRenderRankScoreTable(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	err := renderRankScoreTable(&b, []rankScoreRow{{Label: "v0", Model: "m1", Sum: 8, Count: 2, N: 2, FirstCount: 0, LastCount: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "v0") || !strings.Contains(b.String(), "4.00") {
		t.Errorf("table = %q", b.String())
	}
}

func TestRunRankScoreEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.json")
	key := []rankKeyEntry{
		{Set: 1, Packet: "A", Label: "v0", Model: "m1", Probe: "p"},
		{Set: 1, Packet: "B", Label: "v1", Model: "m1", Probe: "p"},
	}
	data, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, keyPath, string(data))
	reviewsPath := filepath.Join(dir, "r1.jsonl")
	mustWrite(t, reviewsPath, `{"set":1,"ranking":["B","A"],"writing":{"A":3,"B":5},"why":"A rambles"}`+"\n")

	out := captureStdout(t, func() error {
		return run([]string{"rank-score", "--key", keyPath, "--reviews", reviewsPath, "--detail"})
	})
	for _, want := range []string{"v0", "v1", "why: A rambles"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustReadRankKey(t *testing.T, path string) []rankKeyEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var key []rankKeyEntry
	if err := json.Unmarshal(data, &key); err != nil {
		t.Fatalf("key file %q: %v", data, err)
	}
	return key
}
