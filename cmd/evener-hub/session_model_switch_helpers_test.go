package hub

import (
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/identifier"
)

// setSessionModel switches the session's model the way a client does: through
// the authority-bound SetModelControl, fenced on the session's own instance and
// boot generation. A test session is not served by a daemon, so it carries no
// generation until one is installed here; without one there is no authority to
// fence a switch on and the request is rejected as invalid.
func setSessionModel(t *testing.T, sess *agent.Session, model string) error {
	t.Helper()
	if sess.BootGeneration() == "" {
		sess.SetBootGeneration("test")
	}
	generation := sess.BootGeneration()
	mutationID, err := identifier.NewClientMutationID()
	if err != nil {
		t.Fatalf("new client mutation id: %v", err)
	}
	_, switchErr := sess.SetModelControl(agent.ModelControlRequest{
		Model:                  model,
		ClientMutationID:       mutationID,
		ExpectedInstanceID:     sess.ID(),
		ExpectedBootGeneration: generation,
	})
	return switchErr
}
