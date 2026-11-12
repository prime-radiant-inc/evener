package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// serveLine is one parsed componentlog record from the serve component: its
// message plus the session and instant fields every line must carry.
type serveLine struct {
	message string
	at      string
	session string
}

// parseServeLine reads the fields the stamper emits. A line that does not
// carry both a session and an instant, or that was not tagged by the serve
// component, fails the test.
func parseServeLine(t *testing.T, line string) serveLine {
	t.Helper()
	rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "[serve] ")
	if !ok {
		t.Fatalf("unexpected serve output %q: not a [serve] line", line)
	}
	at := strings.LastIndex(rest, " at=")
	if at < 0 {
		t.Fatalf("unexpected serve output %q: no instant field", line)
	}
	attrs := map[string]string{}
	for part := range strings.FieldsSeq(rest[at+1:]) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			t.Fatalf("unexpected serve output %q: malformed field %q", line, part)
		}
		attrs[key] = value
	}
	if attrs["session"] == "" {
		t.Fatalf("unexpected serve output %q: no session field", line)
	}
	return serveLine{message: rest[:at], at: attrs["at"], session: attrs["session"]}
}

// A daemon's [serve] lines land in whatever sink the process that launched it
// chose, and on a hub that sink is shared with every other daemon. A line that
// names no session belongs to whichever daemon wrote last: "[serve] error:
// model returned empty response after 3 retries" sat as the last line of a hub
// log directly under a "[serve] listening…" line for a DIFFERENT session and
// read as the smoking gun for the session under investigation; disproving it
// took cross-referencing API logs across all three live sessions sharing that
// hub (kata vca1). The daemon is the process that authoritatively knows both
// the session and the moment, so it stamps them itself and every sink is
// correct.
func TestServeLogStampsSessionAndTime(t *testing.T) {
	t.Parallel()
	const (
		session = "033z7k96Nj0LLiLImAqa9s"
		listen  = "127.0.0.1:52341"
	)
	tests := []struct {
		name   string
		at     time.Time
		wantAt string
	}{
		{
			name:   "session and instant accompany the daemon's own words",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 176_000_000, time.UTC),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
		{
			name:   "a whole second still spells its milliseconds",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC),
			wantAt: "2026-07-31T18:26:56.000Z",
		},
		{
			name:   "a trailing-zero millisecond keeps the column's width",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 100_000_000, time.UTC),
			wantAt: "2026-07-31T18:26:56.100Z",
		},
		{
			name:   "sub-millisecond detail is dropped, never rounded up",
			at:     time.Date(2026, 7, 31, 18, 26, 56, 176_999_999, time.UTC),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
		{
			name:   "a clock in another zone reports the same instant in UTC",
			at:     time.Date(2026, 8, 1, 3, 26, 56, 176_000_000, time.FixedZone("JST", 9*60*60)),
			wantAt: "2026-07-31T18:26:56.176Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			serveLogAt(&out, tc.at, session, "listening on %s", listen)
			line := parseServeLine(t, out.String())
			if line.at != tc.wantAt {
				t.Errorf("instant = %q, want %q", line.at, tc.wantAt)
			}
			if line.session != session {
				t.Errorf("session = %q, want %q", line.session, session)
			}
			if line.message != "listening on "+listen {
				t.Errorf("message = %q, want the daemon's listening diagnostic", line.message)
			}
		})
	}
}

// Two lines stamped a millisecond apart have to spell the instant in the same
// number of columns, or an operator scanning a log for the moment something
// happened is reading a ragged edge. RFC3339Nano trims trailing zeros and
// would ripple it.
func TestServeLogStampIsFixedWidth(t *testing.T) {
	t.Parallel()
	instants := []time.Time{
		time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC),
		time.Date(2026, 7, 31, 18, 26, 56, 1_000_000, time.UTC),
		time.Date(2026, 7, 31, 18, 26, 56, 100_000_000, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC),
	}
	width := -1
	for _, at := range instants {
		var out bytes.Buffer
		serveLogAt(&out, at, "s", "x")
		line := parseServeLine(t, out.String())
		got := len(line.at)
		if width == -1 {
			width = got
			continue
		}
		if got != width {
			t.Fatalf("instant %q is %d columns, want %d: the timestamp column is ragged", line.at, got, width)
		}
	}
}

// serveLogSources are the daemon files that speak the "[serve]" vocabulary.
// Every line they emit has to carry its session and its instant, so none of
// them may write the bare prefix directly: the whole point of stamping at the
// source is that there is no unattributed path out.
var serveLogSources = []string{"serve.go", "serve_log.go"}

// A stamped line is only worth having if every [serve] line is one. The
// stamper is one call away from being bypassed by an ordinary Fprintf, and a
// bypassed line reads exactly like the anonymous ones that cost a whole
// cross-session investigation to disprove (kata vca1). The literal prefix is
// the tell, so this refuses to let it back into the source.
func TestEveryServeLineGoesThroughTheStamper(t *testing.T) {
	t.Parallel()
	for _, name := range serveLogSources {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if before, _, found := bytes.Cut(src, []byte(`"[serve`)); found {
			line := 1 + bytes.Count(before, []byte("\n"))
			t.Fatalf("%s:%d writes a bare \"[serve]\" prefix: route it through serveLogf so it carries its session and instant", name, line)
		}
	}
}
