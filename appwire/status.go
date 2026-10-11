package appwire

func IsActiveThreadStatus(status string) bool {
	return status == ThreadStatusActive
}

// IsRestingThreadStatus reports a session resting between turns: idle, or
// resting on a failed turn (systemError, which a daemon reports from a failed
// turn until the next turn starts). Controls that belong to a rested session
// -- releasing a queue a Stop parked, the note-save wake warning -- apply to
// both.
func IsRestingThreadStatus(status string) bool {
	return status == ThreadStatusIdle || status == ThreadStatusSystemError
}

func IsActiveTurnStatus(status string) bool {
	return status == TurnStatusInProgress
}

func IsTerminalTurnStatus(status string) bool {
	switch status {
	case TurnStatusCompleted, TurnStatusFailed, TurnStatusInterrupted:
		return true
	default:
		return false
	}
}

func IsActiveItemStatus(status string) bool {
	return status == TurnStatusInProgress
}

func IsTerminalItemStatus(status string) bool {
	switch status {
	case TurnStatusCompleted, TurnStatusFailed, TurnStatusInterrupted:
		return true
	default:
		return false
	}
}
