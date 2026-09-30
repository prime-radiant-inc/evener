package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/execsupport/shellquote"
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
	// A bare model (as --fast-cheap-model allows) means the main model's
	// provider; respond --model needs provider/model.
	if !strings.Contains(model, "/") {
		if provider, _, ok := strings.Cut(cfg.model, "/"); ok {
			model = provider + "/" + model
		}
	}

	self, err := evenerFluencyExecutablePath()
	if err != nil {
		return "", fmt.Errorf("resolve evener-fluency executable: %w", err)
	}

	return shellquote.Args(self, "respond", "--brief-file", briefPath, "--model", model, "--log", logPath), nil
}

// applyAskExchanges attaches a person-driven probe's --ask-responder
// question/answer pairs to its result: AskUserCalls (already computed from
// the run's transcript, res.CanonicalToolCounts) and every pair
// personAskResponderCommand's --log wrote to asks.jsonl beside the work
// dir. A task with no person: block is left untouched. A missing log yields
// a zero count and no asks; but when the run made ask_user calls and yet the
// log holds no asks (missing, empty, or unreadable), that is a failed
// responder, so an infra finding points at the probe's stderr, where the
// responder's own error is printed, rather than letting the failure vanish.
func applyAskExchanges(res *probeResult, probe probeFile) {
	if probe.Person == nil {
		return
	}
	res.AskUserCalls = res.CanonicalToolCounts["ask_user"]
	logPath := filepath.Join(filepath.Dir(res.WorkDir), "asks.jsonl")
	asks, malformed, err := readAskLog(logPath)
	res.Asks = asks
	if err != nil {
		res.Findings = append(res.Findings, finding{
			Category: "infra",
			Title:    "ask log unreadable",
			Detail:   fmt.Sprintf("read %s: %v; the responder's failure is printed in %s", logPath, err, res.StderrPath),
		})
	} else if res.AskUserCalls > 0 && len(asks) == 0 {
		res.Findings = append(res.Findings, finding{
			Category: "infra",
			Title:    "responder logged no asks",
			Detail:   fmt.Sprintf("the run made %d ask_user call(s) but %s holds no asks; the responder's failure is printed in %s", res.AskUserCalls, logPath, res.StderrPath),
		})
	}
	if malformed > 0 {
		res.Findings = append(res.Findings, finding{
			Category: "infra",
			Title:    "ask log has malformed lines",
			Detail:   fmt.Sprintf("%d line(s) of %s did not parse and are missing from asks", malformed, logPath),
		})
	}
}
