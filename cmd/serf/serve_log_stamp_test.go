package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

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
	const session = "033z7k96Nj0LLiLImAqa9s"
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{
			name: "session and instant precede the daemon's own words",
			at:   time.Date(2026, 7, 31, 18, 26, 56, 176_000_000, time.UTC),
			want: "[serve 2026-07-31T18:26:56.176Z session=033z7k96Nj0LLiLImAqa9s] listening on 127.0.0.1:52341\n",
		},
		{
			name: "a whole second still spells its milliseconds",
			at:   time.Date(2026, 7, 31, 18, 26, 56, 0, time.UTC),
			want: "[serve 2026-07-31T18:26:56.000Z session=033z7k96Nj0LLiLImAqa9s] listening on 127.0.0.1:52341\n",
		},
		{
			name: "a trailing-zero millisecond keeps the column's width",
			at:   time.Date(2026, 7, 31, 18, 26, 56, 100_000_000, time.UTC),
			want: "[serve 2026-07-31T18:26:56.100Z session=033z7k96Nj0LLiLImAqa9s] listening on 127.0.0.1:52341\n",
		},
		{
			name: "sub-millisecond detail is dropped, never rounded up",
			at:   time.Date(2026, 7, 31, 18, 26, 56, 176_999_999, time.UTC),
			want: "[serve 2026-07-31T18:26:56.176Z session=033z7k96Nj0LLiLImAqa9s] listening on 127.0.0.1:52341\n",
		},
		{
			name: "a clock in another zone reports the same instant in UTC",
			at:   time.Date(2026, 8, 1, 3, 26, 56, 176_000_000, time.FixedZone("JST", 9*60*60)),
			want: "[serve 2026-07-31T18:26:56.176Z session=033z7k96Nj0LLiLImAqa9s] listening on 127.0.0.1:52341\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			serveLogAt(&out, tc.at, session, "listening on %s", "127.0.0.1:52341")
			if out.String() != tc.want {
				t.Fatalf("stamped line:\n got %q\nwant %q", out.String(), tc.want)
			}
		})
	}
}

// Two lines stamped a millisecond apart have to occupy the same number of
// columns, or an operator scanning a log for the moment something happened is
// reading a ragged edge. RFC3339Nano trims trailing zeros and would ripple it.
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
		got := len(out.String())
		if width == -1 {
			width = got
			continue
		}
		if got != width {
			t.Fatalf("stamp %q is %d columns, want %d: the timestamp column is ragged", out.String(), got, width)
		}
	}
}

// serveLogSources are the daemon files that speak the "[serve] …" vocabulary.
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
		if i := strings.Index(string(src), `"[serve] `); i >= 0 {
			line := 1 + strings.Count(string(src)[:i], "\n")
			t.Fatalf("%s:%d writes a bare \"[serve] \" prefix: route it through serveLogf so it carries its session and instant", name, line)
		}
	}
}
