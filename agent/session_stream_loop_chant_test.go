package agent

import (
	"strings"
	"testing"
)

// TestStreamContentChant_RepeatedPhrase_Trips pins kata fixture (c): a
// reasoning-only runaway chanting a short phrase with zero tool calls
// (cline #13041: DeepSeek-V4-Flash, "Let me" x2865). The repeat count here
// is reduced from the documented ~2865 for test speed; what matters is that
// a short phrase repeated far past chantThreshold trips, fast.
func TestStreamContentChant_RepeatedPhrase_Trips(t *testing.T) {
	c := newStreamContentChant()
	var trip *loopTrip
	for i := 0; i < 300 && trip == nil; i++ {
		trip = c.observe("Let me think about this carefully and check the file. ")
	}
	if trip == nil {
		t.Fatal("expected a chant trip; got none after 300 repeats of a short phrase")
	}
	if trip.Kind != loopTripChant {
		t.Fatalf("trip.Kind = %v, want loopTripChant", trip.Kind)
	}
}

// TestStreamContentChant_LongDistinctProse_NoTrip is the plain negative:
// ordinary varied prose, long enough to exceed the chunk size many times
// over, must never trip.
func TestStreamContentChant_LongDistinctProse_NoTrip(t *testing.T) {
	c := newStreamContentChant()
	words := strings.Fields(`the quick brown fox jumps over the lazy dog while a second
		fox watches from the tree line and a third fox trots past the old barn near
		the river where the water runs cold in autumn and warm in the height of
		summer when the crops stand tall across the valley floor beneath a sky full
		of slow moving clouds that drift toward the distant mountains every single
		afternoon without fail as the season turns from green to gold and back again`)
	for i := 0; i < 40; i++ {
		delta := words[i%len(words)] + " " + words[(i+7)%len(words)] + " "
		if trip := c.observe(delta); trip != nil {
			t.Fatalf("delta %d: unexpected trip %+v on ordinary prose", i, trip)
		}
	}
}

// TestStreamContentChant_UniquePeriodsGuard_NoTrip pins the false-positive
// guard the corrected writeup calls "load-bearing, not polish": a repeated
// short PREFIX (e.g. a numbered-list template) followed by long, mutually
// distinct text each time must not trip, because the text BETWEEN
// occurrences of the repeated chunk is not itself repeating.
func TestStreamContentChant_UniquePeriodsGuard_NoTrip(t *testing.T) {
	c := newStreamContentChant()
	prefix := "### Finding\n\nSeverity: medium. Recommendation: review this section carefully before merging.\n\n"
	distinctBodies := []string{
		"The auth handler skips a nil check on the session token when the request arrives over the legacy header.",
		"The retry loop does not reset its backoff counter after a successful attempt, so a later failure waits far too long.",
		"The cache key omits the tenant id, so two tenants can collide on the same cached response under load.",
		"The migration script assumes UTC but the source table stores local time, so the backfill is off by the timezone offset.",
		"The export path writes a temp file without a random suffix, so two concurrent exports clobber each other's output.",
		"The websocket handler never unregisters a closed connection from the broadcast set, leaking one entry per disconnect.",
		"The config loader silently ignores a malformed line instead of failing the load, so a typo disables a feature quietly.",
		"The pagination cursor encodes an offset instead of a key, so a delete between pages skips or repeats a row.",
		"The rate limiter buckets by IP alone, so a shared NAT exhausts the limit for every user behind it at once.",
		"The health check only pings the primary replica, so a failed secondary never surfaces until a failover is attempted.",
		"The template escapes HTML but not the attribute context, so a crafted value can still break out of a quoted attribute.",
		"The log redaction list is case sensitive, so an uppercase secret key name slips through unredacted into the logs.",
	}
	var trip *loopTrip
	for i, body := range distinctBodies {
		trip = c.observe(prefix + body + "\n\n")
		if trip != nil {
			t.Fatalf("finding %d: unexpected trip %+v; the repeated prefix shares no repeating content between occurrences", i, trip)
		}
	}
}

// TestStreamContentChant_IdenticalPeriodsGuard_Trips is the guard's positive
// case, run at the same chunk/threshold scale as the unique-periods test
// above: the text between occurrences of the repeated chunk is ITSELF
// repeating (a true chant), which the guard must allow through.
func TestStreamContentChant_IdenticalPeriodsGuard_Trips(t *testing.T) {
	c := newStreamContentChant()
	unit := "### Finding\n\nSeverity: medium. Recommendation: review this section carefully before merging.\n\n"
	var trip *loopTrip
	for i := 0; i < 12 && trip == nil; i++ {
		trip = c.observe(unit)
	}
	if trip == nil {
		t.Fatal("expected a chant trip: the same unit repeated verbatim with nothing distinct between occurrences")
	}
}

// TestStreamContentChant_CodeFenceResetsTracking pins kata fixture (e):
// legitimate long code output with the SAME snippet repeated across several
// separate fenced code blocks must not trip -- code-fence resets are
// "load-bearing, not polish" per the corrected writeup.
func TestStreamContentChant_CodeFenceResetsTracking(t *testing.T) {
	c := newStreamContentChant()
	snippet := "func Add(a, b int) int {\n\treturn a + b\n}\n"
	files := []string{
		"internal/mathutil/add.go", "pkg/calc/sum.go", "cmd/tool/helpers.go",
		"internal/report/tally.go", "pkg/stats/reduce.go", "cmd/serve/util.go",
		"internal/graph/weights.go", "pkg/queue/merge.go", "cmd/build/steps.go",
		"internal/cache/evict.go", "pkg/sched/plan.go", "cmd/deploy/roll.go",
		"internal/index/scan.go", "pkg/store/kv.go", "cmd/migrate/apply.go",
		"internal/proxy/route.go", "pkg/auth/token.go", "cmd/watch/tail.go",
		"internal/render/tmpl.go", "pkg/log/fields.go",
	}
	var trip *loopTrip
	for i, path := range files {
		trip = c.observe("The same small helper belongs in " + path + " as well, unchanged:\n\n")
		if trip != nil {
			t.Fatalf("block %d prose: unexpected trip %+v", i, trip)
		}
		trip = c.observe("```go\n" + snippet + "```\n\n")
		if trip != nil {
			t.Fatalf("block %d fenced code: unexpected trip %+v (fenced content must not accumulate into the chant buffer)", i, trip)
		}
	}
}
