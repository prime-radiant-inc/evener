package agent

import (
	"time"

	"primeradiant.com/evener/llm"
)

// providerAdapterTimeout resolves the validated session policy for auxiliary
// calls as well as their retries. It imposes no total request deadline.
func (s *Session) providerAdapterTimeout() *llm.AdapterTimeout {
	idle, _ := ParseProviderIdleTimeout(s.cfg.ProviderIdleTimeout)
	return &llm.AdapterTimeout{Connect: 10 * time.Second, StreamRead: idle}
}
