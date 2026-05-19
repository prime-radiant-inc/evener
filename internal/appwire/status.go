package appwire

import "strings"

func CanonicalThreadStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "", ThreadStatusNotLoaded, "ended":
		return ThreadStatusNotLoaded
	case ThreadStatusIdle:
		return ThreadStatusIdle
	case ThreadStatusActive, "processing":
		return ThreadStatusActive
	case ThreadStatusSystemError, "error":
		return ThreadStatusSystemError
	case ThreadStatusAwaiting:
		return ThreadStatusAwaiting
	case ThreadStatusWarning:
		return ThreadStatusWarning
	case ThreadStatusClosed:
		return ThreadStatusClosed
	default:
		return status
	}
}

func IsActiveThreadStatus(status string) bool {
	return CanonicalThreadStatus(status) == ThreadStatusActive
}

func CanonicalTurnStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "", TurnStatusCompleted:
		return TurnStatusCompleted
	case TurnStatusInProgress, "running", "active", "processing":
		return TurnStatusInProgress
	case TurnStatusInterrupted, "canceled", "cancelled":
		return TurnStatusInterrupted
	case TurnStatusFailed:
		return TurnStatusFailed
	default:
		return status
	}
}

func CanonicalItemStatus(status string) string {
	return CanonicalTurnStatus(status)
}

func IsActiveTurnStatus(status string) bool {
	return CanonicalTurnStatus(status) == TurnStatusInProgress
}

func IsTerminalTurnStatus(status string) bool {
	switch CanonicalTurnStatus(status) {
	case TurnStatusCompleted, TurnStatusFailed, TurnStatusInterrupted:
		return true
	default:
		return false
	}
}

func IsActiveItemStatus(status string) bool {
	return IsActiveTurnStatus(status)
}

func IsTerminalItemStatus(status string) bool {
	return IsTerminalTurnStatus(status)
}
