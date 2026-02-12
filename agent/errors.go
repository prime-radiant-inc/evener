package agent

import "errors"

var ErrPermissionDenied = errors.New("permission denied")

type PermissionDeniedError struct {
	Tool    string
	Message string
}

func (e *PermissionDeniedError) Error() string {
	return "permission denied for tool " + e.Tool + ": " + e.Message
}

func (e *PermissionDeniedError) Is(target error) bool {
	return target == ErrPermissionDenied
}
