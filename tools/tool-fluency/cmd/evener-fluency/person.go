package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// evenerFluencyExecutablePath resolves the path to this running binary, so
// personAskResponderCommand can point --ask-responder at its own "respond"
// subcommand without a separate install. Tests replace it with a fixed path.
var evenerFluencyExecutablePath = os.Executable

// personAskResponderCommand builds the --ask-responder command for a probe
// whose task manifest carries a person: block: this binary's own "respond"
// subcommand, given the person's brief (written to a file beside the run's
// work/state directories, matching stdoutPath/stderrPath's own convention)
// and the model that plays them. --log points respond at a file in the same
// run directory so the harness can read back every question/answer pair
// (readAskLog, applied in runProbe).
func personAskResponderCommand(cfg runConfig, probe probeFile, res probeResult) (string, error) {
	runDir := filepath.Dir(res.WorkDir)
	briefPath := filepath.Join(runDir, "person-brief.txt")
	if err := os.WriteFile(briefPath, []byte(probe.Person.Brief), 0o644); err != nil {
		return "", fmt.Errorf("write person brief: %w", err)
	}
	logPath := filepath.Join(runDir, "asks.jsonl")

	model := strings.TrimSpace(probe.Person.Model)
	if model == "" {
		model = strings.TrimSpace(cfg.fastCheapModel)
	}
	if model == "" {
		model = cfg.model
	}

	self, err := evenerFluencyExecutablePath()
	if err != nil {
		return "", fmt.Errorf("resolve evener-fluency executable: %w", err)
	}

	return fmt.Sprintf("%s respond --brief-file %s --model %s --log %s",
		shellQuote(self), shellQuote(briefPath), shellQuote(model), shellQuote(logPath)), nil
}

// applyAskExchanges attaches a person-driven probe's --ask-responder
// question/answer pairs to its result: AskUserCalls (already computed from
// the run's transcript, res.CanonicalToolCounts) and every pair
// personAskResponderCommand's --log wrote to asks.jsonl beside the work
// dir. A task with no person: block is left untouched, and a missing or
// unreadable log (the responder never ran) yields a zero count and no
// asks rather than an error.
func applyAskExchanges(res *probeResult, probe probeFile) {
	if probe.Person == nil {
		return
	}
	res.AskUserCalls = res.CanonicalToolCounts["ask_user"]
	logPath := filepath.Join(filepath.Dir(res.WorkDir), "asks.jsonl")
	res.Asks = readAskLog(logPath)
}

// shellQuote wraps s in single quotes for safe inclusion in a command string
// that a later `sh -c` will parse (evener run's --ask-responder), escaping
// any single quote already in s.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
