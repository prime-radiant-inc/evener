package agent

import "errors"

// ErrPermissionDenied is the sentinel error indicating that permission was denied.
var ErrPermissionDenied = errors.New("permission denied")

// PermissionDeniedError records that permission was denied for a specific tool,
// along with an explanatory message.
type PermissionDeniedError struct {
	Tool    string // name of the tool whose use was denied
	Message string // human-readable reason for the denial
}

// Error returns the error message describing the tool and reason for which
// permission was denied.
func (e *PermissionDeniedError) Error() string {
	return "permission denied for tool " + e.Tool + ": " + e.Message
}

// Is reports whether target is ErrPermissionDenied, allowing PermissionDeniedError
// to match ErrPermissionDenied via errors.Is.
func (e *PermissionDeniedError) Is(target error) bool {
	return target == ErrPermissionDenied
}
