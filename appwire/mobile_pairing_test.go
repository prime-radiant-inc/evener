package appwire

import (
	"testing"
)

func TestMobilePairingMethodIsHubScoped(t *testing.T) {
	for _, method := range Methods {
		if method.Name != MethodEvenerMobilePairing {
			continue
		}
		if method.Scope != ScopeHub {
			t.Fatalf("mobile pairing scope = %q, want %q", method.Scope, ScopeHub)
		}
		if _, ok := method.Params.(MobilePairingParams); !ok {
			t.Fatalf("mobile pairing params = %T, want MobilePairingParams", method.Params)
		}
		if _, ok := method.Result.(MobilePairingResponse); !ok {
			t.Fatalf("mobile pairing result = %T, want MobilePairingResponse", method.Result)
		}
		return
	}
	t.Fatalf("method catalog missing %q", MethodEvenerMobilePairing)
}
