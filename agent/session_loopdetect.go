package agent

import "strings"

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
		case "high":
			s.cfg.ReasoningEffort = "xhigh"
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
