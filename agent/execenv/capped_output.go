package execenv

import "primeradiant.com/evener/agent/internal/cappedoutput"

// outputOmissionMarker joins the retained head and tail of a stream whose
// capture tripped the cap. The grep arms render their own cuts with it too, so
// every capture regime discloses missing bytes the same way.
const outputOmissionMarker = cappedoutput.OmissionMarker

// maxCapturedOutputBytes bounds the stdout and the stderr one buffered command
// captures per stream (#4557): a firehose like `yes` fills an unbounded buffer
// at pipe speed well inside the command timeout, which a container memory cap
// turns into a SIGKILL. It is the shared capture's default, which matches
// grepRetainedBytes (the grep arms' own retained-output budget) and the
// streamed jobs path's retention budget, so every capture regime costs the same
// order of memory.
const maxCapturedOutputBytes = cappedoutput.DefaultCapBytes
