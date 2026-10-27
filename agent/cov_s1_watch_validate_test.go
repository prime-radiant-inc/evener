package agent

import (
	"errors"
	"testing"
)

// validateWatchSendDeliveryTarget rejects the non-delegate delivery targets
// before any store read: a missing delegate handle is a target_not_found, and
// the other rejections are not.
func TestS1Cov_validateWatchSendDeliveryTarget_EarlyArms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		target       string
		wantErr      bool
		wantNotFound bool
	}{
		{"empty", "", true, false},
		{"caller_alias", runtimeMessageAliasCaller, false, false},
		{"watched_alias", runtimeMessageAliasWatched, true, false},
		{"main", "main", true, true},
		{"star", "*", true, true},
		{"job_handle", "job_abc", true, false},
		{"non_delegate_token", "something", true, true},
		{"unknown_delegate", "dlg_ghost", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWatchSendDeliveryTarget(tc.target)
			if (err != nil) != tc.wantErr {
				t.Fatalf("target %q: err = %v, want error %v", tc.target, err, tc.wantErr)
			}
			if errors.Is(err, errWatchTargetNotFound) != tc.wantNotFound {
				t.Fatalf("target %q: not-found = %v, want %v", tc.target, !tc.wantNotFound, tc.wantNotFound)
			}
		})
	}
}
