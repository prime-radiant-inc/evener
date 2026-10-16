package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
		AuthToken: "mobile-secret",
		PublicURL: "https://hub.example.test:9443",
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

func TestPublicURLMobilePairingCanonicalOrigin(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTPS://BÜCHER.EXAMPLE:443/", "https://xn--bcher-kva.example"},
		{"https://[2001:DB8::20]:443/", "https://[2001:db8::20]"},
	} {
		response, err := mobilePairing(hubcore.WebConfig{AuthToken: "synthetic-token", PublicURL: tc.raw}, appwire.MobilePairingParams{})
		if err != nil || response.AuthURL != tc.want+"/auth/synthetic-token" {
			t.Fatalf("origin %q: URL %q, err %v", tc.raw, response.AuthURL, err)
		}
	}
}

func TestHubMobilePairingDiscoversListenerOrigin(t *testing.T) {
	for _, tt := range []struct {
		name, bind, origin, want string
		addresses                []string
	}{
		{"bound LAN", "192.168.1.20:9180", "http://localhost:19181", "http://192.168.1.20:9180", nil},
		{"bound IPv6", "[fd00::20]:9180", "http://[::1]:19181", "http://[fd00::20]:9180", nil},
		{"MagicDNS browser", "0.0.0.0:9180", "http://magic-kingdom:9180", "http://magic-kingdom:9180", []string{"192.168.1.20/24", "100.87.98.83/32"}},
		{"bound public", "203.0.113.20:9180", "", "http://203.0.113.20:9180", nil},
		{"wildcard from localhost", "[::]:9180", "http://localhost:19181", "http://100.87.98.83:9180", []string{"fd00::20/64", "100.87.98.83/32", "192.168.1.20/24"}},
		{"LAN without Tailscale", ":9180", "", "http://192.168.1.20:9180", []string{"127.0.0.1/8", "fe80::1/64", "192.168.1.20/24"}},
		{"IPv4 listener skips IPv6", "0.0.0.0:9180", "", "http://192.168.1.20:9180", []string{"fd00::20/64", "192.168.1.20/24"}},
		{"public interface", "0.0.0.0:9180", "", "http://203.0.113.20:9180", []string{"203.0.113.20/24"}},
		{"private before public", "[::]:9180", "", "http://[fd00::20]:9180", []string{"203.0.113.20/24", "fd00::20/64"}},
		{"loopback stays local", "127.0.0.1:9180", "http://localhost:9180", "", []string{"100.87.98.83/32"}},
		{"no usable interface", "[::]:9180", "http://localhost:9180", "", []string{"127.0.0.1/8", "fe80::1/64"}},
		{"reachable browser wins", "0.0.0.0:9180", "https://hub.example.test", "https://hub.example.test", []string{"100.87.98.83/32"}},
		{"proxy to loopback", "127.0.0.1:9180", "https://hub.example.test", "https://hub.example.test", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{AuthToken: "mobile-secret", HubAddr: tt.bind,
				MobileRouteAddrs: func() []netip.Addr { return nil },
				MobileInterfaceAddrs: func() ([]net.Addr, error) {
					var addresses []net.Addr
					for _, raw := range tt.addresses {
						ip, network, err := net.ParseCIDR(raw)
						if err != nil {
							t.Fatal(err)
						}
						network.IP = ip
						addresses = append(addresses, network)
					}
					return addresses, nil
				},
			}
			response, err := dispatchMobilePairing(t, NewWebServer(cfg), appwire.MobilePairingParams{Origin: tt.origin})
			if tt.want == "" {
				var wireErr appwire.WireError
				if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
					t.Fatalf("error = %v, want conflict", err)
				}
				if response.AuthURL != "" {
					t.Fatal("unreachable listener issued a pairing URL")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.AuthURL != tt.want+"/auth/mobile-secret" {
				t.Fatalf("auth URL = %q, want origin %q", response.AuthURL, tt.want)
			}
		})
	}
}

func TestHubMobilePairingPrefersRoutedInterfaceOverContainerBridge(t *testing.T) {
	for _, route := range []string{"192.168.1.20", "203.0.113.20", "fd00::20"} {
		t.Run(route, func(t *testing.T) {
			for _, tailnet := range []bool{false, true} {
				cfg := hubcore.WebConfig{AuthToken: "mobile-secret", HubAddr: "[::]:9180",
					MobileRouteAddrs: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr(route)} },
					MobileInterfaceAddrs: func() ([]net.Addr, error) {
						addresses := []net.Addr{
							&net.IPNet{IP: net.ParseIP("172.17.0.1"), Mask: net.CIDRMask(16, 32)},
							&net.IPNet{IP: net.ParseIP(route), Mask: net.CIDRMask(128, 128)},
						}
						if tailnet {
							addresses = append(addresses, &net.IPNet{IP: net.ParseIP("100.87.98.83"), Mask: net.CIDRMask(32, 32)})
						}
						return addresses, nil
					},
				}
				response, err := dispatchMobilePairing(t, NewWebServer(cfg), appwire.MobilePairingParams{})
				if err != nil {
					t.Fatal(err)
				}
				want := route
				if tailnet {
					want = "100.87.98.83"
				}
				if response.AuthURL != "http://"+net.JoinHostPort(want, "9180")+"/auth/mobile-secret" {
					t.Fatalf("auth URL = %q; want interface %s (tailnet %t)", response.AuthURL, want, tailnet)
				}
			}
		})
	}
}

func TestHubMobilePairingRejectsUnsafeOriginWithoutLeakingToken(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "mobile-secret"})

	_, err := dispatchMobilePairing(t, web, appwire.MobilePairingParams{
		Origin: "http://127.0.0.1:9180",
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeConflict, wireErr)
	}
	if strings.Contains(wireErr.Message, "mobile-secret") {
		t.Fatalf("unreachable response leaked token: %q", wireErr.Message)
	}
}

func TestHubMobilePairingRejectsUnsafeConfiguredBaseURL(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		AuthToken: "mobile-secret",
		PublicURL: "http://127.0.0.1:9180",
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

func TestPublicURLUnsafeInvitationDoesNotDisableDirectHub(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "synthetic-token", PublicURL: "http://127.0.0.1:9180"})
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/api/debug/subscriptions")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uncredentialed direct request status %d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/debug/subscriptions", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-token")
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated direct request status %d", response.StatusCode)
	}
	if _, err := mobilePairing(hubcore.WebConfig{AuthToken: "synthetic-token", PublicURL: "http://127.0.0.1:9180"}, appwire.MobilePairingParams{}); err == nil {
		t.Fatal("unsafe mobile origin permitted")
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

func TestSafeMobileOriginAllowsHTTPDestinations(t *testing.T) {
	for _, origin := range []string{
		"http://magic-kingdom:9180", "http://hub.example.test:9180",
		"http://203.0.113.20:9180", "http://[2001:db8::20]:9180",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); !ok || got != origin {
				t.Fatalf("safeMobileOrigin(%q) = %q, %v; want unchanged origin", origin, got, ok)
			}
		})
	}
}

func TestSafeMobileOriginRejectsNonPortableAddresses(t *testing.T) {
	for _, origin := range []string{
		"http://0.0.0.0:9180", "https://0.0.0.0", "http://[::]:9180", "https://[::]",
		"http://[fe80::1%25en0]:9180", "https://[fd00::20%25en0]:9180",
	} {
		t.Run(origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(origin); ok {
				t.Fatalf("safeMobileOrigin(%q) = %q, true; want rejection", origin, got)
			}
		})
	}
}

func TestSafeMobileOriginAllowsOrdinaryHTTPSDNSNameWithoutResolution(t *testing.T) {
	for _, tc := range []struct{ origin, want string }{
		{"https://unresolvable.example.test:9443", "https://unresolvable.example.test:9443"},
		{"https://bücher.example:9443", "https://xn--bcher-kva.example:9443"},
		{"https://notlocalhost", "https://notlocalhost"},
		{"https://foo_bar.example:9443", "https://foo_bar.example:9443"},
		{"https://-foo.example:9443", "https://-foo.example:9443"},
		{"https://foo-.example:9443", "https://foo-.example:9443"},
		{"https://123.example:9443", "https://123.example:9443"},
		{"https://hub123:9443", "https://hub123:9443"},
		{"https://hub.0xgg:9443", "https://hub.0xgg:9443"},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			if got, ok := safeMobileOrigin(tc.origin); !ok || got != tc.want {
				t.Fatalf("safeMobileOrigin(%q) = %q, %v; want %q", tc.origin, got, ok, tc.want)
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
