package main

// rank.go implements rank-sets and rank-score: the blind side-by-side read
// that has driven every prompt-version comparison so far, promoted out of
// hand-written jq and a pasted subagent prompt.
//
// rank-sets turns review-pack's packets into ranking sets, one per (model,
// task, repetition), with one packet per label. Within a set the packets
// carry no label, only a letter (A, B, C, ...) in random order, so a reviewer
// ranks them on writing alone. rank-score reads the reviewers' rankings back
// and unblinds them against rank-sets' own key (never against review-pack's
// key directly: a reviewer only ever sees letters, and rank-sets' key is what
// maps a letter in a given set back to a label).

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/cmdutil"
)

// defaultRankReviewerPrompt is the versioned editor-judgment instructions
// rank-sets hands every reviewer, unless --prompt names a different file.
const defaultRankReviewerPrompt = "tools/prompt-eval/rank-reviewer-prompt-v1.md"

// defaultSkipTasks lists the tasks rank-sets leaves out of every ranking set
// unless --skip-task names a different set. prose.smoke only proves the model
// answered at all; there is no writing in it to rank.
var defaultSkipTasks = []string{"prose.smoke"}

// rankPacketLetters are the identifiers rank-sets assigns packets within one
// set. A set holding more versions than this has no letter left to assign.
const rankPacketLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// rankGroupKey identifies one ranking set before its packets are ordered:
// every packet a reviewer compares answers the same task, from the same
// model, on the same repetition.
type rankGroupKey struct {
	Model      string
	Probe      string
	Repetition int
}

// groupForRanking groups review-pack's key entries into ranking sets,
// dropping any entry whose task is in skipTasks. It refuses when one group
// would hold two packets for the same label: rank-sets has no way to choose
// between them.
func groupForRanking(entries []reviewEntry, skipTasks map[string]bool) (map[rankGroupKey][]reviewEntry, error) {
	groups := map[rankGroupKey][]reviewEntry{}
	seenLabel := map[rankGroupKey]map[string]reviewEntry{}
	for _, e := range entries {
		if skipTasks[e.Probe] {
			continue
		}
		k := rankGroupKey{Model: e.Model, Probe: e.Probe, Repetition: e.Repetition}
		if seenLabel[k] == nil {
			seenLabel[k] = map[string]reviewEntry{}
		}
		if prior, ok := seenLabel[k][e.Label]; ok {
			return nil, fmt.Errorf("model=%s task=%s repetition=%d has two packets for label %q: %s (%s) and %s (%s); rank-sets needs at most one packet per label in a set",
				e.Model, e.Probe, e.Repetition, e.Label, prior.Packet, prior.Result, e.Packet, e.Result)
		}
		seenLabel[k][e.Label] = e
		groups[k] = append(groups[k], e)
	}
	return groups, nil
}

// sortedRankGroupKeys orders a ranking-set grouping deterministically, so the
// same key file always numbers its sets the same way.
func sortedRankGroupKeys(groups map[rankGroupKey][]reviewEntry) []rankGroupKey {
	keys := make([]rankGroupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Model != keys[j].Model {
			return keys[i].Model < keys[j].Model
		}
		if keys[i].Probe != keys[j].Probe {
			return keys[i].Probe < keys[j].Probe
		}
		return keys[i].Repetition < keys[j].Repetition
	})
	return keys
}

// assignLetters orders entries randomly (seeded by rng) and returns them in
// that order; the caller assigns rankPacketLetters by position. Entries are
// sorted by label first so the shuffle's input is deterministic given rng.
func assignLetters(entries []reviewEntry, rng *rand.Rand) ([]reviewEntry, error) {
	if len(entries) > len(rankPacketLetters) {
		return nil, fmt.Errorf("a set has %d packets, more than the %d letters rank-sets can assign", len(entries), len(rankPacketLetters))
	}
	ordered := slices.Clone(entries)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Label < ordered[j].Label })
	rng.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
	return ordered, nil
}

// rankKeyEntry is one row of rank-sets' own key: the letter a reviewer saw
// inside one set, and the label/model/task/repetition it names. rank-score
// reads this file to unblind reviewers' rankings; a reviewer never sees it.
type rankKeyEntry struct {
	Set        int    `json:"set"`
	Packet     string `json:"packet"`
	Label      string `json:"label"`
	Model      string `json:"model"`
	Probe      string `json:"probe"`
	Repetition int    `json:"repetition"`
}

// rankSetPacket is one labeled packet placed into a set, in the order the
// reviewer will see it.
type rankSetPacket struct {
	ID      string
	Entry   reviewEntry
	Content string
}

// rankSet is one set a reviewer ranks: every packet answers the same task,
// from the same model and repetition.
type rankSet struct {
	Number     int
	Model      string
	Probe      string
	Repetition int
	Packets    []rankSetPacket
}

// buildRankSets groups entries into ranking sets, reads each chosen packet's
// content from packetsDir, and returns the sets in set-number order, the key
// that unblinds them, and a note for every group rank-sets left out because it
// held fewer than two versions (nothing to rank).
func buildRankSets(entries []reviewEntry, packetsDir string, skipTasks map[string]bool, rng *rand.Rand) ([]rankSet, []rankKeyEntry, []string, error) {
	groups, err := groupForRanking(entries, skipTasks)
	if err != nil {
		return nil, nil, nil, err
	}
	var sets []rankSet
	var key []rankKeyEntry
	var notes []string
	number := 0
	for _, k := range sortedRankGroupKeys(groups) {
		group := groups[k]
		if len(group) < 2 {
			notes = append(notes, fmt.Sprintf("skipped model=%s task=%s repetition=%d: only %d version(s), nothing to rank",
				k.Model, k.Probe, k.Repetition, len(group)))
			continue
		}
		ordered, err := assignLetters(group, rng)
		if err != nil {
			return nil, nil, nil, err
		}
		number++
		set := rankSet{Number: number, Model: k.Model, Probe: k.Probe, Repetition: k.Repetition}
		for i, entry := range ordered {
			id := string(rankPacketLetters[i])
			content, err := os.ReadFile(filepath.Join(packetsDir, entry.Packet))
			if err != nil {
				return nil, nil, nil, err
			}
			set.Packets = append(set.Packets, rankSetPacket{ID: id, Entry: entry, Content: string(content)})
			key = append(key, rankKeyEntry{Set: number, Packet: id, Label: entry.Label, Model: entry.Model, Probe: entry.Probe, Repetition: entry.Repetition})
		}
		sets = append(sets, set)
	}
	return sets, key, notes, nil
}

// rankAnswerInstructions is the mechanical part of what rank-sets hands a
// reviewer: how to write back a ranking, in the letter-based format the code
// produces. The editorial judgment itself lives in the versioned reviewer
// prompt file, since that is the part expected to change with experience.
const rankAnswerInstructions = `## How to answer

Read every packet in a set before ranking it. For each set, on its own line, write one JSON object:

{"set": <set number>, "ranking": ["B", "A", "C"], "writing": {"A": 4, "B": 5, "C": 2}, "why": "one sentence, quoting the worst packet"}

"ranking" lists every packet's letter in the set, best writing first. "writing" scores every packet 1-5 (3 good, 2 acceptable, 1 poor). "why" says the single most important thing about the set and quotes the worst packet.
`

// renderRankSets renders the reviewer prompt, the answer format, and every
// set, in set order, as the plain text file rank-sets hands a reviewer.
func renderRankSets(prompt string, sets []rankSet) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(prompt, "\n"))
	b.WriteString("\n\n")
	b.WriteString(rankAnswerInstructions)
	for _, set := range sets {
		fmt.Fprintf(&b, "\n## Set %d (model=%s, task=%s)\n\n", set.Number, set.Model, set.Probe)
		for _, p := range set.Packets {
			fmt.Fprintf(&b, "### Packet %s\n\n%s\n\n", p.ID, strings.TrimRight(p.Content, "\n"))
		}
	}
	return b.String()
}

func runRankSets(args []string) error {
	fs := flag.NewFlagSet("rank-sets", flag.ContinueOnError)
	reviewPackKey := fs.String("review-pack-key", "", "review-pack's key.json")
	packets := fs.String("packets", "", "review-pack's packets directory")
	out := fs.String("out", "", "plain text file of ranking sets, to hand a reviewer")
	keyOut := fs.String("key", "", "rank-sets' own key: which letter named which label in each set; never hand this to a reviewer")
	promptPath := fs.String("prompt", defaultRankReviewerPrompt, "reviewer judgment instructions")
	var skipTask cmdutil.StringSliceFlag
	fs.Var(&skipTask, "skip-task", "task id to leave out of every set (repeatable; default: "+strings.Join(defaultSkipTasks, ", ")+")")
	seed := fs.Uint64("seed", uint64(time.Now().UnixNano()), "shuffle seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *reviewPackKey == "" || *packets == "" || *out == "" || *keyOut == "" {
		return errors.New("--review-pack-key, --packets, --out, and --key are required")
	}
	skip := []string(skipTask)
	if len(skip) == 0 {
		skip = defaultSkipTasks
	}
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
	}

	data, err := os.ReadFile(*reviewPackKey)
	if err != nil {
		return err
	}
	var entries []reviewEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("%s: %w", *reviewPackKey, err)
	}
	prompt, err := os.ReadFile(*promptPath)
	if err != nil {
		return fmt.Errorf("reviewer prompt: %w", err)
	}

	rng := rand.New(rand.NewPCG(*seed, *seed))
	sets, key, notes, err := buildRankSets(entries, *packets, skipSet, rng)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return errors.New("no ranking sets: every task was skipped or every group had fewer than two versions")
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "rank-sets:", n)
	}

	if err := os.WriteFile(*out, []byte(renderRankSets(string(prompt), sets)), 0o644); err != nil {
		return err
	}
	keyData, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*keyOut, append(keyData, '\n'), 0o644)
}

// reviewLine is one reviewer's ranking of one set, as they write it back: one
// JSON object per line, matching rankAnswerInstructions.
type reviewLine struct {
	Set     int            `json:"set"`
	Ranking []string       `json:"ranking"`
	Writing map[string]int `json:"writing"`
	Why     string         `json:"why"`
}

// readReviewLines parses one reviewer's JSON-lines output, skipping blank
// lines. A line that fails to parse names its 1-based line number.
func readReviewLines(path string) ([]reviewLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	var out []reviewLine
	seenSets := map[int]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rl reviewLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		// scoreReviews aggregates every reviewLine it sees, so a set
		// repeated within this one file would silently double-count this
		// reviewer's judgment. A different file (a different reviewer)
		// covering the same set is fine and expected; that is checked here,
		// per file, before runRankScore merges them together.
		if seenSets[rl.Set] {
			return nil, fmt.Errorf("%s: set %d appears twice", path, rl.Set)
		}
		seenSets[rl.Set] = true
		out = append(out, rl)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// rankScoreRow is one (label, model)'s aggregate over every reviewed set it
// appeared in.
type rankScoreRow struct {
	Label      string `json:"label"`
	Model      string `json:"model"`
	Sum        int    `json:"-"`
	Count      int    `json:"-"`
	FirstCount int    `json:"first_count"`
	LastCount  int    `json:"last_count"`
	N          int    `json:"n"`
}

// Mean is the row's mean writing score.
func (r rankScoreRow) Mean() float64 {
	if r.Count == 0 {
		return 0
	}
	return float64(r.Sum) / float64(r.Count)
}

// MarshalJSON reports Mean alongside the row's other fields, since Sum and
// Count are internal accumulators, not part of the reported shape.
func (r rankScoreRow) MarshalJSON() ([]byte, error) {
	type shape struct {
		Label      string  `json:"label"`
		Model      string  `json:"model"`
		Mean       float64 `json:"mean"`
		FirstCount int     `json:"first_count"`
		LastCount  int     `json:"last_count"`
		N          int     `json:"n"`
	}
	return json.Marshal(shape{Label: r.Label, Model: r.Model, Mean: r.Mean(), FirstCount: r.FirstCount, LastCount: r.LastCount, N: r.N})
}

// scoreReviews unblinds every review line against key and aggregates mean
// score, first-place count, and last-place count per (label, model). It
// refuses a line that names a set not in the key, a ranking that is not
// exactly a permutation of that set's packets, a writing score for a packet
// not in the set or missing for one that is, or a score outside 1-5: a
// reviewer's mistake here should fail loudly, not silently skew the count.
func scoreReviews(key []rankKeyEntry, reviews []reviewLine) ([]rankScoreRow, error) {
	bySet := map[int]map[string]rankKeyEntry{}
	for _, k := range key {
		if bySet[k.Set] == nil {
			bySet[k.Set] = map[string]rankKeyEntry{}
		}
		bySet[k.Set][k.Packet] = k
	}
	type rowKey struct{ label, model string }
	rows := map[rowKey]*rankScoreRow{}
	for _, rl := range reviews {
		set, ok := bySet[rl.Set]
		if !ok {
			return nil, fmt.Errorf("set %d: not in the key", rl.Set)
		}
		if len(rl.Ranking) != len(set) {
			return nil, fmt.Errorf("set %d: ranking has %d packets, want %d", rl.Set, len(rl.Ranking), len(set))
		}
		seen := make(map[string]bool, len(rl.Ranking))
		for _, id := range rl.Ranking {
			if seen[id] {
				return nil, fmt.Errorf("set %d: packet %q ranked twice", rl.Set, id)
			}
			seen[id] = true
			if _, ok := set[id]; !ok {
				return nil, fmt.Errorf("set %d: ranking names unknown packet %q", rl.Set, id)
			}
		}
		for id, score := range rl.Writing {
			if _, ok := set[id]; !ok {
				return nil, fmt.Errorf("set %d: writing names unknown packet %q", rl.Set, id)
			}
			if score < 1 || score > 5 {
				return nil, fmt.Errorf("set %d: packet %q scored %d, want 1-5", rl.Set, id, score)
			}
		}
		for id := range set {
			if _, ok := rl.Writing[id]; !ok {
				return nil, fmt.Errorf("set %d: writing is missing packet %q", rl.Set, id)
			}
		}
		for i, id := range rl.Ranking {
			entry := set[id]
			rk := rowKey{entry.Label, entry.Model}
			row := rows[rk]
			if row == nil {
				row = &rankScoreRow{Label: entry.Label, Model: entry.Model}
				rows[rk] = row
			}
			row.Sum += rl.Writing[id]
			row.Count++
			row.N++
			switch i {
			case 0:
				row.FirstCount++
			case len(rl.Ranking) - 1:
				row.LastCount++
			}
		}
	}
	out := make([]rankScoreRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b rankScoreRow) int {
		if c := strings.Compare(a.Label, b.Label); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out, nil
}

// renderRankScoreTable prints one row per (label, model): mean writing
// score, times ranked first, times ranked last, and the sample size behind
// them.
func renderRankScoreTable(w io.Writer, rows []rankScoreRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "LABEL\tMODEL\tMEAN\tFIRST\tLAST\tN")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%.2f\t%d\t%d\t%d\n", r.Label, r.Model, r.Mean(), r.FirstCount, r.LastCount, r.N)
	}
	return tw.Flush()
}

// renderRankScoreDetail lists every reviewed set's unblinded ranking and its
// "why", in set order. scoreReviews must have already validated reviews
// against key: every id it names is guaranteed present.
func renderRankScoreDetail(w io.Writer, key []rankKeyEntry, reviews []reviewLine) {
	bySet := map[int]map[string]rankKeyEntry{}
	meta := map[int]rankKeyEntry{}
	for _, k := range key {
		if bySet[k.Set] == nil {
			bySet[k.Set] = map[string]rankKeyEntry{}
		}
		bySet[k.Set][k.Packet] = k
		meta[k.Set] = k
	}
	sorted := slices.Clone(reviews)
	slices.SortFunc(sorted, func(a, b reviewLine) int { return a.Set - b.Set })
	for _, rl := range sorted {
		set := bySet[rl.Set]
		m := meta[rl.Set]
		labels := make([]string, 0, len(rl.Ranking))
		for _, id := range rl.Ranking {
			labels = append(labels, set[id].Label)
		}
		_, _ = fmt.Fprintf(w, "Set %d (model=%s, task=%s): %s\n  why: %s\n", rl.Set, m.Model, m.Probe, strings.Join(labels, " > "), rl.Why)
	}
}

func runRankScore(args []string) error {
	fs := flag.NewFlagSet("rank-score", flag.ContinueOnError)
	keyPath := fs.String("key", "", "rank-sets' key.json")
	var reviewFiles cmdutil.StringSliceFlag
	fs.Var(&reviewFiles, "reviews", "a reviewer's JSON-lines output (repeatable)")
	detail := fs.Bool("detail", false, "also list each set's unblinded ranking and why")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || len(reviewFiles) == 0 {
		return errors.New("--key and at least one --reviews file are required")
	}
	data, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	var key []rankKeyEntry
	if err := json.Unmarshal(data, &key); err != nil {
		return fmt.Errorf("%s: %w", *keyPath, err)
	}

	var reviews []reviewLine
	for _, path := range reviewFiles {
		lines, err := readReviewLines(path)
		if err != nil {
			return err
		}
		reviews = append(reviews, lines...)
	}
	rows, err := scoreReviews(key, reviews)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return err
		}
	} else if err := renderRankScoreTable(os.Stdout, rows); err != nil {
		return err
	}
	if *detail {
		renderRankScoreDetail(os.Stdout, key, reviews)
	}
	return nil
}
