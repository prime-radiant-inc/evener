package agent

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/internal/tool"
)

// failureLoopExcerptRunes bounds the failure excerpt carried by the structural
// intervention, so a huge tool output cannot swamp the steering message.
const failureLoopExcerptRunes = 500

// stuckEscalation returns the steering message for the nth loop detection.
// First detection bumps reasoning effort; subsequent detections get increasingly
// direct about abandoning the current approach.
func (s *Session) stuckEscalation(count int) string {
	switch count {
	case 1:
		// Bump reasoning effort to help the agent think harder.
		s.mu.Lock()
		prev := s.cfg.ReasoningEffort
		switch prev {
		case "", "low", "medium":
			s.cfg.ReasoningEffort = "high"
		case "high", "xhigh":
			// "max" is the top of serf's effort lattice; the per-model clamp
			// lowers it to whatever tier the model actually tops out at.
			s.cfg.ReasoningEffort = "max"
		}
		s.mu.Unlock()
		return "You are stuck in a loop. Your reasoning effort has been increased. " +
			"Stop and think about why your current approach is not working. " +
			"What assumption are you making that might be wrong?"
	case 2:
		return "You are still stuck. Your current approach is fundamentally not working. " +
			"Abandon it completely and try a different strategy. " +
			"What is the simplest possible way to achieve the goal?"
	default:
		return "You have been stuck for a long time. " +
			"If you cannot make progress, report what you tried and what failed."
	}
}

// looksLikeQuestion returns true when the assistant text appears to be asking
// the user a question or requesting input (ends with "?" or ":").
func looksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, ":")
}

// failureLoopIntervention is the steering for a detected loop whose whole window
// failed. Thinking harder cannot help when nothing in the window succeeded, so
// this names the structural problem — a repeated failing call — and shows the
// most recent failure instead of escalating reasoning effort.
func failureLoopIntervention(signatures []string, windowSize int, lastFailure string) string {
	return fmt.Sprintf(
		"Every one of the last %d tool calls failed, and they repeat the same pattern: %s. "+
			"Repeating a failing call cannot make it succeed. The most recent failure was:\n\n%s\n\n"+
			"Stop. Either change the arguments, or take a different approach to the goal.",
		windowSize,
		strings.Join(windowToolNames(signatures, windowSize), ", "),
		tool.TruncateRunes(lastFailure, failureLoopExcerptRunes),
	)
}

// windowToolNames returns the distinct tool names in the last windowSize
// signatures, in first-seen order. Signatures are "name:argsHash".
func windowToolNames(signatures []string, windowSize int) []string {
	if windowSize <= 0 || len(signatures) < windowSize {
		windowSize = len(signatures)
	}
	seen := make(map[string]bool, windowSize)
	names := make([]string, 0, windowSize)
	for _, sig := range signatures[len(signatures)-windowSize:] {
		name := sig
		if i := strings.LastIndex(sig, ":"); i >= 0 {
			name = sig[:i]
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// allFailed reports whether every one of the last windowSize tool calls failed.
// A single success inside the window means the run contains real progress, so
// the loop is not a pure failure loop.
func allFailed(failed []bool, windowSize int) bool {
	if windowSize <= 0 || len(failed) < windowSize {
		return false
	}
	for _, f := range failed[len(failed)-windowSize:] {
		if !f {
			return false
		}
	}
	return true
}

// detectLoop checks the last windowSize tool call signatures for repeating
// patterns of length 1, 2, or 3.
func detectLoop(signatures []string, windowSize int) bool {
	if len(signatures) < windowSize {
		return false
	}
	recent := signatures[len(signatures)-windowSize:]
	for patLen := 1; patLen <= 3; patLen++ {
		if windowSize%patLen != 0 {
			continue
		}
		pattern := recent[:patLen]
		allMatch := true
		for i := patLen; i < windowSize; i++ {
			if recent[i] != pattern[i%patLen] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}
