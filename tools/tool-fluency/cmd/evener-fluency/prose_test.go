package main

import "testing"

func TestCountProse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		text string
		want proseCounts
	}{
		{"plain", "The build passes now.", proseCounts{Words: 4}},
		{"em dash", "It works — mostly.", proseCounts{Words: 3, EmDashes: 1}},
		{"comma not", "It is a guide, not a rule.", proseCounts{Words: 7, Contrastive: 1}},
		{"isn't it's", "It isn't slow, it's blocked.", proseCounts{Words: 5, Contrastive: 1}},
		{"plain negation", "Do not merge yet.", proseCounts{Words: 4}},
		{"bold label, colon inside", "- **Status:** done", proseCounts{Words: 2, BoldLabels: 1}},
		{"bold label, colon outside", "1. **Status**: done", proseCounts{Words: 3, BoldLabels: 1}},
		{"bold without a label", "- done **now**", proseCounts{Words: 2}},
		{"header", "## Summary\nDone.", proseCounts{Words: 2, Headers: 1}},
		{"arrow", "A → B", proseCounts{Words: 2, Arrows: 1}},
		{"arrow in code", "Run `a -> b` now.", proseCounts{Words: 2}},
		{"shouting", "NEVER push. Never mind NASA.", proseCounts{Words: 5, Shouting: 1}},
		{"identifiers",
			"Fixed #123 in Task 4 and F2 at 3f9a2c1 via job_034OOL8H87MqrpJOhRqsGI_a1B2c3D4e5F6 in session 034OOL8H87MqrpJOhRqsGI.",
			proseCounts{Words: 14, OpaqueIDs: 6}},
		{"delegate id", "Delegate dlg_02wMz5Txv2enqVTitaig6F reported back.", proseCounts{Words: 4, OpaqueIDs: 1}},
		{"tool names", "Check job_status, then job_send_message and dlg_status.", proseCounts{Words: 6}},
		{"not identifiers", "The decade deadbeef release 1.2.3 cost 1234567 dollars.", proseCounts{Words: 10}},
		{"fenced code", "Done.\n```\nx -> y — #12\n```", proseCounts{Words: 1}},
	} {
		if got := countProse(c.text); got != c.want {
			t.Errorf("%s: countProse(%q) = %+v, want %+v", c.name, c.text, got, c.want)
		}
	}
}

func TestProseCountsAdd(t *testing.T) {
	t.Parallel()
	a := proseCounts{Words: 3, EmDashes: 1, OpaqueIDs: 2}
	a.add(proseCounts{Words: 4, Contrastive: 1, OpaqueIDs: 1})
	if want := (proseCounts{Words: 7, EmDashes: 1, Contrastive: 1, OpaqueIDs: 3}); a != want {
		t.Fatalf("add = %+v, want %+v", a, want)
	}
}
