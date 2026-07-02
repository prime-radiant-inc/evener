//go:build serffuzz

package agent

import (
	"sort"
	"strings"
	"testing"
)

// ctParseNames rebuilds a name slice from a NUL-delimited fuzz string.
func ctParseNames(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// ctParseNameMap rebuilds a canonical->provider map from NUL-delimited "k=v"
// entries under the real ToolNameMap invariants, so reverse resolution
// (provider -> canonical) is single-valued and deterministic. Keys and values
// are trimmed and empties dropped; values are unique (canonical <-> provider is
// a bijection, so the reverse scan cannot return different keys under map
// iteration order); and value set stays disjoint from key set (single-hop
// resolution). Input NAME slices are left arbitrary so the trim/drop-empty
// logic is still exercised.
func ctParseNameMap(s string) map[string]string {
	type entry struct{ k, v string }
	var raw []entry
	keys := map[string]bool{}
	if s != "" {
		for _, kv := range strings.Split(s, "\x00") {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				continue
			}
			k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if k == "" || v == "" || keys[k] {
				continue
			}
			keys[k] = true
			raw = append(raw, entry{k, v})
		}
	}
	m := make(map[string]string, len(raw))
	usedValues := map[string]bool{}
	for _, e := range raw {
		if usedValues[e.v] || keys[e.v] {
			continue // keep values unique and disjoint from keys
		}
		usedValues[e.v] = true
		m[e.k] = e.v
	}
	return m
}

func ctFirstIndex(xs []string, target string) int {
	for i, x := range xs {
		if x == target {
			return i
		}
	}
	return -1
}

// ctAssertCleanList checks a resolved name list has no empties and no duplicates.
func ctAssertCleanList(t *testing.T, out []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, n := range out {
		if n == "" {
			t.Fatalf("empty name in output %v", out)
		}
		if seen[n] {
			t.Fatalf("duplicate %q in output %v", n, out)
		}
		seen[n] = true
	}
}

// FuzzCtCanonicalizeToolNames drives canonicalizeToolNames — the order-preserving
// dedup/drop-empty core lifted out of the Session method. Oracles: determinism;
// no empties or duplicates; len(out) <= len(in); idempotence; set completeness
// against the per-element canonical forms; and first-seen order preservation.
func FuzzCtCanonicalizeToolNames(f *testing.F) {
	f.Add("read\x00shell\x00read", "read=rd\x00shell=sh")
	f.Add("", "")
	f.Add("  \x00a\x00a\x00", "a=b")
	f.Add("rd\x00sh\x00rd\x00unknown", "read=rd\x00shell=sh")

	f.Fuzz(func(t *testing.T, namesRaw, mapRaw string) {
		names := ctParseNames(namesRaw)
		nameMap := ctParseNameMap(mapRaw)

		out := canonicalizeToolNames(names, nameMap)
		if out2 := canonicalizeToolNames(names, nameMap); !ctEqual(out, out2) {
			t.Fatalf("non-deterministic: %v vs %v", out, out2)
		}
		ctAssertCleanList(t, out)
		if len(out) > len(names) {
			t.Fatalf("len(out)=%d > len(in)=%d", len(out), len(names))
		}
		if again := canonicalizeToolNames(out, nameMap); !ctEqual(again, out) {
			t.Fatalf("not idempotent: %v vs %v", again, out)
		}

		// The per-element non-empty canonical forms, in order.
		var canon []string
		for _, n := range names {
			if c := canonicalToolName(n, nameMap); c != "" {
				canon = append(canon, c)
			}
		}
		// Completeness: out and canon cover the same set.
		for _, c := range canon {
			if ctFirstIndex(out, c) < 0 {
				t.Fatalf("canonical %q missing from output %v", c, out)
			}
		}
		for _, o := range out {
			if ctFirstIndex(canon, o) < 0 {
				t.Fatalf("output %q not a canonical form of any input", o)
			}
		}
		// First-seen order: consecutive outputs keep their canon first-index order.
		for i := 1; i < len(out); i++ {
			if ctFirstIndex(canon, out[i-1]) >= ctFirstIndex(canon, out[i]) {
				t.Fatalf("order not first-seen preserved: %v", out)
			}
		}
	})
}

// FuzzCtProviderVisibleToolNames drives providerVisibleToolNames — the sorted
// dedup/drop-empty core. Oracles: determinism; no empties or duplicates;
// len(out) <= len(in); sorted ascending; idempotence.
func FuzzCtProviderVisibleToolNames(f *testing.F) {
	f.Add("read\x00shell\x00read", "read=rd\x00shell=sh")
	f.Add("", "")
	f.Add("z\x00a\x00m\x00a", "a=alpha")
	f.Add("read\x00write\x00exec", "read=rd\x00write=wr")

	f.Fuzz(func(t *testing.T, namesRaw, mapRaw string) {
		names := ctParseNames(namesRaw)
		nameMap := ctParseNameMap(mapRaw)

		out := providerVisibleToolNames(names, nameMap)
		if out2 := providerVisibleToolNames(names, nameMap); !ctEqual(out, out2) {
			t.Fatalf("non-deterministic: %v vs %v", out, out2)
		}
		ctAssertCleanList(t, out)
		if len(out) > len(names) {
			t.Fatalf("len(out)=%d > len(in)=%d", len(out), len(names))
		}
		if !sort.StringsAreSorted(out) {
			t.Fatalf("output not sorted: %v", out)
		}
		if again := providerVisibleToolNames(out, nameMap); !ctEqual(again, out) {
			t.Fatalf("not idempotent: %v vs %v", again, out)
		}
	})
}

func ctEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
