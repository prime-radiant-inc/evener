package hub

import (
	"io"
	"os"
)

// hubDiagnosticSink, when set, receives the hub's own diagnostic lines, such as
// the roster-refresh failure that follows a spawn. It is nil in production,
// where hubDiagnosticOutput resolves os.Stderr at each call exactly as before.
// A test sets it to own the diagnostics a failure-injection fixture provokes,
// and restores it when the test ends.
var hubDiagnosticSink io.Writer

// hubDiagnosticOutput is where the hub's own diagnostic lines go: a test's sink
// when one is installed, otherwise os.Stderr.
func hubDiagnosticOutput() io.Writer {
	if hubDiagnosticSink != nil {
		return hubDiagnosticSink
	}
	return os.Stderr
}
