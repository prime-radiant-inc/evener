package dev

// timing-render prints the human `go test` text that a `go test -json` stream
// carries in its `output` and `build-output` events — the second is where the
// pinned toolchain reports compiler diagnostics for a package that fails to
// build, and they reach nowhere else. It exists so the gate's single measured run
// (issue #144) can capture durations from `-json` — which forces verbose test
// output — while the module log the gate keeps for its failure dump still
// reads the way a plain `go test` run's would: the JSON must not erase the
// human diagnostics (issue #144's "JSON reporting must not erase human
// diagnostics" risk).
//
// It is deliberately separate from timing-parse: that one turns the stream
// into the ratchet's TSV rows, this one turns it back into text. Both are
// testable against JSON stream strings rather than through a faked toolchain
// (docs/developing-evener/testing.md's port-on-touch rule).

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// renderEvent is the subset of `go test -json`'s event shape timing-render
// reads: the Action and the Output text it carries.
type renderEvent struct {
	Action string
	Output string
}

func timingRenderMain(args []string) int {
	return runTimingRender(args, os.Stdout, os.Stderr)
}

func runTimingRender(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("timing-render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonPath := fs.String("json", "", "path to the go test -json stream")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev timing-render --json FILE\n\n"+
			"Print the human go test text a go test -json stream carries in its\n"+
			"output and build-output events, so a run that captures durations from\n"+
			"-json still leaves the readable log a plain go test run would.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *jsonPath == "" {
		fs.Usage()
		return 2
	}
	f, err := os.Open(*jsonPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-render: %v\n", err)
		return 1
	}
	defer func() { _ = f.Close() }()
	if err := renderGoTestJSON(f, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev timing-render: %v\n", err)
		return 1
	}
	return 0
}

// renderableActions are the `go test -json` actions whose Output text belongs to
// the human log. "output" carries a test's own lines; "build-output" carries the
// compiler's diagnostics when a package fails to build — the pinned toolchain
// reports them only there, and writes nothing to stderr for them, so a renderer
// that read only "output" turned a build failure into a bare `[build failed]`
// line with no explanation (issue #144 review).
var renderableActions = map[string]bool{"output": true, "build-output": true}

// renderGoTestJSON writes the Output text of every renderable event in a
// `go test -json` stream. A truncated or malformed line is skipped rather than
// failing the render: it is one lost log line, not a reason to lose the rest of
// the log.
func renderGoTestJSON(r io.Reader, w io.Writer) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, readErr := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			var ev renderEvent
			if err := json.Unmarshal([]byte(trimmed), &ev); err == nil && renderableActions[ev.Action] && ev.Output != "" {
				if _, werr := io.WriteString(w, ev.Output); werr != nil {
					return fmt.Errorf("writing rendered output: %w", werr)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("reading go test -json stream: %w", readErr)
		}
	}
	return nil
}
