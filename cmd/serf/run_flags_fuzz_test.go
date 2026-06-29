package main

import (
	"io"
	"strings"
	"testing"
)

// FuzzRunFlagParse drives serf's real CLI argument parser. newRunFlagSet builds
// the production flag set (including the repeatable StringSliceFlag values), and
// fs.Parse is the seam that turns argv into the runConfig. Parsing only — run()
// is never invoked, so there is no agent execution, network, or FS access.
// Oracle: no-panic floor — flag.ContinueOnError must surface every malformed
// argv as an error, never a crash.
func FuzzRunFlagParse(f *testing.F) {
	seeds := []string{
		"--model openai/gpt-5.5 do the thing",
		"--max-rounds 50 --verbose",
		"--max-rounds notanint",
		"--reasoning-effort high --resume 01ABC",
		"--skills-dir a --skills-dir b --mcp x:cmd --plugin-dir p",
		"--list-sessions",
		"--unknown-flag value",
		"--max-subagent-depth -1 --share-task-store",
		"-h",
		"",
		"--model",
		"--system-prompt-append a --system-prompt-append b",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		var args []string
		if raw != "" {
			args = strings.Split(raw, " ")
		}
		fs, _ := newRunFlagSet(io.Discard)
		// Parsing must never panic; an error is the expected failure mode.
		_ = fs.Parse(args)
	})
}
