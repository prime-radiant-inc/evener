package llm

import (
	"context"
	"fmt"
	"sync"
)

var (
	registryMu     sync.RWMutex
	protocols      = map[string]Protocol{}
	authenticators = map[string]Authenticator{}
)

// RegisterProtocol registers the single implementation of a wire protocol
// under p.ID() (spec §8.1). Registering an id twice panics: two packages
// claiming one protocol is a build mistake, not a runtime condition.
func RegisterProtocol(p Protocol) {
	id := p.ID()
	if id == "" {
		panic("llm: RegisterProtocol: empty protocol id")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := protocols[id]; dup {
		panic(fmt.Sprintf("llm: RegisterProtocol: protocol %q registered twice", id))
	}
	protocols[id] = p
}

// ProtocolFor returns the registered protocol for an id.
func ProtocolFor(id string) (Protocol, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := protocols[id]
	return p, ok
}

// RegisterAuthenticator registers the implementation of one auth scheme
// (registry.AuthBearer, registry.AuthGCPADC, ...). Registering a scheme
// twice panics for the same reason RegisterProtocol does.
func RegisterAuthenticator(scheme string, a Authenticator) {
	if scheme == "" {
		panic("llm: RegisterAuthenticator: empty scheme")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := authenticators[scheme]; dup {
		panic(fmt.Sprintf("llm: RegisterAuthenticator: scheme %q registered twice", scheme))
	}
	authenticators[scheme] = a
}

// AuthenticatorFor returns the registered authenticator for a scheme.
func AuthenticatorFor(scheme string) (Authenticator, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := authenticators[scheme]
	return a, ok
}

// AuthenticatorForContext returns the call-scoped authenticator carried
// by ctx (see WithAuthenticatorOverride) when one is set for the call's
// scheme, else the process-global registration for the scheme. A nil
// return means no authenticator is bound for the scheme.
func AuthenticatorForContext(ctx context.Context, scheme string) Authenticator {
	if auth := AuthenticatorOverrideFor(ctx, scheme); auth != nil {
		return auth
	}
	a, _ := AuthenticatorFor(scheme)
	return a
}

// RequestPreparerFor returns the scheme's authenticator as a RequestPreparer
// when it implements the optional interface (spec §8.1: only the Codex
// transport does).
func RequestPreparerFor(scheme string) (RequestPreparer, bool) {
	a, ok := AuthenticatorFor(scheme)
	if !ok {
		return nil, false
	}
	p, ok := a.(RequestPreparer)
	return p, ok
}
