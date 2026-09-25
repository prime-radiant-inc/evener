package transcript

import "primeradiant.com/evener/agent/schema"

// ExecutionTurns maps the TurnID of every execution turn recorded in entries
// (a turn whose first entry carries TurnKind execution) to whether it is
// still open: a turn is open until a completion entry follows its start or
// its latest reopen marker. Legacy entries carry no identity and are not
// counted.
func ExecutionTurns(entries []Entry) map[string]bool {
	open := map[string]bool{}
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Format == 0 || turn.TurnID == "" {
			continue
		}
		if turn.TurnKind == schema.TurnSpanExecution {
			open[turn.TurnID] = true
		}
		if _, execution := open[turn.TurnID]; !execution {
			continue
		}
		switch turn.Kind {
		case schema.TurnCompletion:
			open[turn.TurnID] = false
		case schema.TurnReopen:
			open[turn.TurnID] = true
		}
	}
	return open
}
