package agent

import (
	"errors"
	"testing"
)

func TestPermissionDeniedError(t *testing.T) {
	t.Parallel()
	err := &PermissionDeniedError{
		Tool:    "shell",
		Message: "command not allowed by policy",
	}
	expected := "permission denied for tool shell: command not allowed by policy"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
	if !errors.Is(err, ErrPermissionDenied) {
		t.Error("PermissionDeniedError should match ErrPermissionDenied via errors.Is")
	}
}
