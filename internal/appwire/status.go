package appwire

func IsActiveThreadStatus(status string) bool {
	return status == ThreadStatusActive
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
