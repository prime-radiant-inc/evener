package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

// newManagerPromptRoot fixes host-derived prompt inputs while retaining the real
// Manager session lifecycle and request composition.
func newManagerPromptRoot(t *testing.T, adapter *messagingAdapter, configure func(*SessionConfig)) *Session {
	t.Helper()
	return newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.environmentInfo = func(execenv.ExecutionEnvironment, clock.Clock) schema.EnvironmentInfo {
			return schema.EnvironmentInfo{}
		}
		cfg.testOnly.sandboxProber = bwrapCapableProber(t.TempDir())
		if configure != nil {
			configure(cfg)
		}
	})
}
