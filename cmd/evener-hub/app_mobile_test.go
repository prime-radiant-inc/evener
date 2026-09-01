package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubMobilePairingUsesExplicitSafeOrigin(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})

	response, err := dispatchMobilePairing(t, web, appwire.MobilePairingParams{
		Origin: "http://192.168.1.20:9180",
	})
	if err != nil {
		t.Fatalf("dispatch mobile pairing: %v", err)
	}
	if response.AuthURL != "http://192.168.1.20:9180/auth/mobile-secret" {
		t.Fatalf("auth URL = %q", response.AuthURL)
	}
}

func TestHubMobilePairingConfiguredBaseURLTakesPrecedence(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		AuthToken:     "mobile-secret",
		MobileBaseURL: "https://hub.example.test:9443",
	})

	response, err := dispatchMobilePairing(t, web, appwire.MobilePairingParams{
		Origin: "http://127.0.0.1:9180",
	})
	if err != nil {
		t.Fatalf("dispatch mobile pairing: %v", err)
	}
	if response.AuthURL != "https://hub.example.test:9443/auth/mobile-secret" {
		t.Fatalf("auth URL = %q", response.AuthURL)
	}
}

func TestHubMobilePairingRejectsUnsafeOriginWithoutLeakingToken(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})

	_, err := dispatchMobilePairing(t, web, appwire.MobilePairingParams{
		Origin: "http://93.184.216.34:9180",
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeConflict, wireErr)
	}
	const want = "mobile pairing requires a reachable non-loopback Hub origin"
	if wireErr.Message != want {
		t.Fatalf("error message = %q, want %q", wireErr.Message, want)
	}
	if strings.Contains(wireErr.Message, "mobile-secret") {
		t.Fatalf("unreachable response leaked token: %q", wireErr.Message)
	}
}

func TestHubMobilePairingRejectsUnsafeConfiguredBaseURL(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		AuthToken:     "mobile-secret",
		MobileBaseURL: "http://127.0.0.1:9180",
	})

	_, err := dispatchMobilePairing(t, web, appwire.MobilePairingParams{
		Origin: "https://hub.example.test",
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeConflict, wireErr)
	}
}

func TestSafeMobileOriginRejectsLoopbackAlternateSpellings(t *testing.T) {
	for _, origin := range []string{
		"https://localhost",
		"https://LOCALHOST.",
		"https://LoCaLhOsT.",
		"https://foo.localhost",
		"https://FoO.LoCaLhOsT.",
		"https://ｆｏｏ．ｌｏｃａｌｈｏｓｔ",
		"https://127.1",
		"https://2130706433",
		"https://0x7f000001",
		"https://１２７.０.０.１",
		"https://１２７．１",
		"https://０ｘ７ｆ０００００１",
		"https://²¹³⁰⁷⁰⁶⁴³³",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); ok {
				t.Fatalf("safeMobileOrigin(%q) = %q, true; want rejection", origin, got)
			}
		})
	}
}

func TestSafeMobileOriginAllowsOrdinaryHTTPSDNSNameWithoutResolution(t *testing.T) {
	for _, origin := range []string{
		"https://unresolvable.example.test:9443",
		"https://bücher.example:9443",
		"https://notlocalhost",
		"https://foo_bar.example:9443",
		"https://-foo.example:9443",
		"https://foo-.example:9443",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); !ok || got != origin {
				t.Fatalf("safeMobileOrigin(%q) = %q, %v; want unchanged origin", origin, got, ok)
			}
		})
	}
}

func TestSafeMobileOriginRejectsInvalidIDNAHostname(t *testing.T) {
	const origin = "https://a\u200db.example"
	if got, ok := safeMobileOrigin(origin); ok {
		t.Fatalf("safeMobileOrigin(%q) = %q, true; want rejection", origin, got)
	}
}

func dispatchMobilePairing(t *testing.T, web *WebServer, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal mobile pairing params: %v", err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerMobilePairing,
		Params: raw,
	})
	if err != nil {
		return appwire.MobilePairingResponse{}, err
	}
	response, ok := result.(appwire.MobilePairingResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.MobilePairingResponse", result)
	}
	return response, nil
}
