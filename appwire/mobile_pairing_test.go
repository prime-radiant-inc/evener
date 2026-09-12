package appwire

import (
	"context"
	"fmt"
	"testing"
)

func TestClientMobilePairingRoundTrip(t *testing.T) {
	written := roundTrip(t, MethodEvenerMobilePairing, MobilePairingResponse{
		AuthURL: "https://hub.example.test/auth/mobile-secret",
	}, func(ctx context.Context, client *Client) error {
		response, err := client.MobilePairing(ctx, MobilePairingParams{
			Origin: "http://192.168.1.20:9180",
		})
		if err != nil {
			return err
		}
		if response.AuthURL != "https://hub.example.test/auth/mobile-secret" {
			return fmt.Errorf("auth URL = %q", response.AuthURL)
		}
		return nil
	})
	assertWrittenParams(t, "MobilePairing", written.Request.Params, `{"origin":"http://192.168.1.20:9180"}`)
}

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
