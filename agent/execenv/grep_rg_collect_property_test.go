package execenv

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rgCollectorCorpus is the record sequence one search emits: each path with the
// payload sizes of its matches, in the order ripgrep emits that path's records.
// The sizes are chosen so the byte budget can bind partway through a path, which
// is where whole-block eviction and record eviction disagree.
var rgCollectorCorpus = []struct {
	path    string
	records []int
}{
	{"f01.txt", []int{30, 30}},
	{"f02.txt", []int{70}},
	{"f03.txt", []int{20}},
	{"f04.txt", []int{40, 40, 40}},
}

// rgCollectorRecords is the corpus as the content-mode records it stands for,
// keyed by path.
func rgCollectorRecords(dir string) map[string][][]byte {
	out := make(map[string][][]byte, len(rgCollectorCorpus))
	for _, p := range rgCollectorCorpus {
		for _, size := range p.records {
			head := dir + string(filepath.Separator) + p.path + "\x001:"
			out[p.path] = append(out[p.path], []byte(head+strings.Repeat("x", size-len(head)-1)+"\n"))
		}
	}
	return out
}

// rgCollectorPathOrderPermutations returns every ordering of paths, so the
// properties below are asserted over all of them rather than over two samples.
func rgCollectorPathOrderPermutations(paths []string) [][]string {
	if len(paths) <= 1 {
		return [][]string{slices.Clone(paths)}
	}
	var out [][]string
	for i := range paths {
		for _, tail := range rgCollectorPathOrderPermutations(slices.Concat(paths[:i], paths[i+1:])) {
			out = append(out, append([]string{paths[i]}, tail...))
		}
	}
	return out
}

// collect runs one emission order through a collector and returns what it
// retained.
func collect(t *testing.T, records map[string][][]byte, order []string, keep, budget int) []byte {
	t.Helper()
	c := newRgRecordCollector("/base", false, keep, budget)
	for _, path := range order {
		for _, raw := range records[path] {
			if _, err := c.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	c.flush()
	return []byte(c.retained())
}

// TestRgCollectorRetentionIsOrderIndependentAndAPrefix is MEDIUM 1 (round 7):
// whole-block byte eviction is not record-granular, so with a tight budget
// ascending emission kept a later file's record that descending emission
// evicted with its block. The property, asserted over EVERY emission order and
// under block-count pressure, byte pressure, and both at once, is that the
// retained records are the same ORDERED PREFIX of records that fits the budget —
// computed here independently of the collector, by walking the canonical path
// order and stopping at the first record the budget cannot hold.
func TestRgCollectorRetentionIsOrderIndependentAndAPrefix(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	records := rgCollectorRecords(dir)
	paths := make([]string, 0, len(rgCollectorCorpus))
	for _, p := range rgCollectorCorpus {
		paths = append(paths, p.path)
	}
	slices.SortFunc(paths, rgPathOrder)

	// want is the ordered prefix of records that fits: the first keep paths, in
	// the collector's path order, walked record by record until the budget stops
	// it.
	want := func(keep, budget int) []byte {
		var out []byte
		total := 0
		for i, path := range paths {
			if i >= keep {
				break
			}
			for _, raw := range records[path] {
				if total+len(raw) > budget {
					return out
				}
				out = append(out, raw...)
				total += len(raw)
			}
		}
		return out
	}

	orders := rgCollectorPathOrderPermutations(paths)
	for _, tc := range []struct {
		name   string
		keep   int
		budget int
	}{
		{name: "no pressure", keep: 8, budget: 1 << 20},
		{name: "byte pressure mid-path", keep: 8, budget: 200},
		{name: "block-count pressure", keep: 2, budget: 1 << 20},
		{name: "both", keep: 2, budget: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expected := want(tc.keep, tc.budget)
			for _, order := range orders {
				if got := collect(t, records, order, tc.keep, tc.budget); !bytes.Equal(got, expected) {
					t.Fatalf("retention depends on emission order or is not the ordered prefix:\n order    = %v\n retained = %q\n want     = %q",
						order, got, expected)
				}
			}
		})
	}
}

// TestRgCollectorDisclosesEveryByteEviction is MEDIUM 2 (round 7): a successful
// byte-budget eviction left truncated() false, so a search that dropped a record
// for size reported itself as complete — invisible whenever the surviving rows
// sit below maxResults. This is an absence-flavored property, so it also asserts
// the converse (a run that evicts nothing discloses nothing), and the mutation
// check is required: dropping the disclosure from the eviction must fail it.
func TestRgCollectorDisclosesEveryByteEviction(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	records := rgCollectorRecords(dir)
	paths := make([]string, 0, len(rgCollectorCorpus))
	for _, p := range rgCollectorCorpus {
		paths = append(paths, p.path)
	}
	slices.SortFunc(paths, rgPathOrder)

	// keep is far past the record count, so nothing is dropped for the count:
	// every omission here is the budget's, and must be disclosed.
	const keep = 64
	const budget = 200
	for _, order := range rgCollectorPathOrderPermutations(paths) {
		c := newRgRecordCollector(dir, false, keep, budget)
		for _, path := range order {
			for _, raw := range records[path] {
				if _, err := c.Write(raw); err != nil {
					t.Fatal(err)
				}
			}
		}
		c.flush()
		if !c.truncated() {
			t.Fatalf("a byte-budget eviction did not disclose truncation (order %v): %q", order, c.retained())
		}
		if lines := len(ripgrepOutputLines(c.retained(), dir, false, false)); lines >= keep {
			t.Fatalf("the retained rows are not below maxResults, so the omission would be visible anyway: %d", lines)
		}
	}

	// The converse: a run whose records all fit discloses nothing, so the
	// property above cannot be satisfied by disclosing unconditionally.
	for _, order := range rgCollectorPathOrderPermutations(paths) {
		c := newRgRecordCollector(dir, false, keep, 1<<20)
		for _, path := range order {
			for _, raw := range records[path] {
				if _, err := c.Write(raw); err != nil {
					t.Fatal(err)
				}
			}
		}
		c.flush()
		if c.truncated() {
			t.Fatalf("a run that evicted nothing disclosed truncation (order %v)", order)
		}
	}
}
