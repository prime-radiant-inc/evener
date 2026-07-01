package agent

import (
	"strings"
	"testing"
)

// validateWatchSendTarget rejects the non-delegate delivery targets before any
// store read, and surfaces target_not_found for an unknown delegate handle.
func TestS1Cov_validateWatchSendTarget_EarlyArms(t *testing.T) {
	jm := newSession(t).jobManager

	tests := []struct {
		name    string
		target  string
		wantErr string // "" means nil
	}{
		{"empty", "", "internal watch delivery target is required"},
		{"caller_alias", runtimeMessageAliasCaller, ""},
		{"watched_alias", runtimeMessageAliasWatched, "watched is not a v1 delivery target"},
		{"main", "main", "target_not_found"},
		{"star", "*", "target_not_found"},
		{"job_handle", "job_abc", "job_id is a job/turn handle"},
		{"non_delegate_token", "something", "target_not_found"},
		{"unknown_delegate", "dlg_ghost", "target_not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := jm.validateWatchSendTarget(tc.target, watchArgs{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("target %q: want nil, got %v", tc.target, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("target %q: want error containing %q, got %v", tc.target, tc.wantErr, err)
			}
		})
	}
}
