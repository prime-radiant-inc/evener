package appwire

// IsActiveThreadStatus reports whether a wire thread status means a turn is
// running.
func IsActiveThreadStatus(status string) bool {
	return threadStatusMachine.IsActive(status)
}

// IsRestingThreadStatus reports a session resting between turns: idle, or
// resting on a failed turn (systemError, which a daemon reports from a failed
// turn until the next turn starts). Controls that belong to a rested session
// -- releasing a queue a Stop parked, the note-save wake warning -- apply to
// both.
func IsRestingThreadStatus(status string) bool {
	state, ok := threadStatusMachine.State(status)
	return ok && state.Resting
}

func IsActiveTurnStatus(status string) bool {
	return turnMachine.IsActive(status)
}

func IsTerminalTurnStatus(status string) bool {
	return turnMachine.IsTerminal(status)
}

func IsActiveItemStatus(status string) bool {
	return IsActiveTurnStatus(status)
}

func IsTerminalItemStatus(status string) bool {
	return IsTerminalTurnStatus(status)
}
