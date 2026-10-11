package transcript

import (
	"regexp"
	"strings"
)

// subagentEndingWords says the daemon's delegate reason codes plainly. It
// mirrors appwire-client's delegateEndingText (delegateDetails.ts), so the TUI
// says a subagent's ending the way the web and phone do.
var subagentEndingWords = map[string]string{
	"failed":                            "failed",
	"run_error":                         "failed with an error",
	"ended_without_report":              "ended without reporting",
	"terminal_error":                    "ended with an error",
	"missing_terminal":                  "ended without reporting",
	"runtime_lost":                      "runtime lost",
	"input_persist_failed":              "couldn't save its input",
	"cancelled":                         "cancelled",
	"stopped_by_parent":                 "stopped by its coordinator",
	"tool_round_budget_exhausted":       "ran out of tool rounds",
	"turn_budget_exhausted":             "ran out of turns",
	"launch_failed":                     "couldn't start",
	"construction_failed":               "couldn't be set up",
	"artifacts_dir_failed":              "couldn't create its artifacts folder",
	"input_admission_failed":            "couldn't take its input",
	"attention_consumed_without_report": "finished without a new report",
}

// subagentOutcomeWords is the word for an outcome whose reason is a code
// the TUI doesn't know.
var subagentOutcomeWords = map[string]string{
	"failed":    "failed",
	"exhausted": "ran out of budget",
	"cancelled": "stopped",
	"stopped":   "stopped",
}

var reasonCode = regexp.MustCompile(`^[a-z0-9]+(?:_[a-z0-9]+)+$`)

// SubagentEndingText is how a subagent's last run ended, in words: its
// error's first line when the hub sent one, else its reason code said
// plainly. A code the TUI doesn't know reads as its outcome's word, so
// snake_case never shows; a reason already in words shows as it is. Empty
// when there is nothing to say.
func SubagentEndingText(run SubagentRunInfo) string {
	if cause, _, _ := strings.Cut(strings.TrimSpace(run.Error), "\n"); strings.TrimSpace(cause) != "" {
		return strings.TrimSpace(cause)
	}
	reason := strings.TrimSpace(run.Reason)
	if reason == "" {
		return ""
	}
	if words, ok := subagentEndingWords[reason]; ok {
		return words
	}
	if !reasonCode.MatchString(reason) {
		return reason
	}
	return subagentOutcomeWords[strings.TrimSpace(run.Outcome)]
}
