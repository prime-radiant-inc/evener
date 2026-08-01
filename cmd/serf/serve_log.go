package main

import (
	"fmt"
	"io"
	"time"
)

// serveLogTimeLayout stamps a [serve] line as RFC3339 in UTC at millisecond
// precision. UTC cross-references directly against the provider API logs and
// the rendezvous started_at values an operator reads alongside it, and the
// fixed-width sub-second field keeps the column straight where RFC3339Nano's
// trailing-zero trimming would ripple it.
const serveLogTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// serveLogf writes one [serve] line labelled with the session it belongs to
// and the moment it happened.
//
// A daemon's output lands in a sink it does not own, and on a hub that sink
// holds every daemon's lines at once. Only this process authoritatively knows
// which session it is and what time it is, so it says both itself rather than
// leaving a reader to infer attribution from whichever line came before
// (kata vca1).
func serveLogf(w io.Writer, sessionID, format string, args ...any) {
	serveLogAt(w, time.Now(), sessionID, format, args...)
}

// serveLogAt is serveLogf against a caller-supplied instant.
func serveLogAt(w io.Writer, at time.Time, sessionID, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "[serve %s session=%s] %s\n",
		at.UTC().Format(serveLogTimeLayout), sessionID, fmt.Sprintf(format, args...))
}
