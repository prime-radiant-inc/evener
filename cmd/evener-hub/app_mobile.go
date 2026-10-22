package hub

import (
	"context"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/hubendpoint"
)

var mobileHostnameProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.CheckHyphens(false))

func registerMobilePairingHandler(server *appserver.Server, cfg hubcore.WebConfig) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMobilePairing, func(_ context.Context, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
		return mobilePairing(cfg, params)
	})
}

func mobilePairing(cfg hubcore.WebConfig, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
	base, ok := mobilePairingOrigin(cfg, params.Origin)
	if !ok {
		return appwire.MobilePairingResponse{}, appwire.Conflict("mobile pairing requires a reachable non-loopback Hub origin")
	}
	return appwire.MobilePairingResponse{AuthURL: hubedge.AuthURLFor(base, cfg.AuthToken)}, nil
}

// safeMobileOrigin accepts HTTP(S) destinations supported by the mobile app.
// Loopback refers to the phone after scanning rather than the Hub, and an
// unspecified listener address does not identify a destination. Scoped IPv6
// addresses cannot be represented in browser URLs or shared across devices.
func safeMobileOrigin(raw string) (string, bool) {
	origin, err := hubendpoint.ParseOrigin(raw)
	if err != nil {
		return "", false
	}
	host := strings.TrimRight(origin.Hostname(), ".")
	if _, err := netip.ParseAddr(host); err != nil {
		// Browsers apply UTS #46 mappings before interpreting a hostname. Do
		// the same so Unicode spellings of numeric addresses cannot bypass the
		// IP and legacy-numeric checks below. Canonical IP literals bypass IDNA
		// because IPv6 colons are not valid domain-name runes.
		host, err = mobileHostnameProfile.ToASCII(host)
		if err != nil {
			return "", false
		}
		host = strings.TrimRight(host, ".")
	}
	// Reject localhost and its reserved subdomains in all case and
	// trailing-dot spellings.
	hostLower := strings.ToLower(host)
	if host == "" || hostLower == "localhost" || strings.HasSuffix(hostLower, ".localhost") {
		return "", false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", false
		}
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsUnspecified() {
			return "", false
		}
	} else if hasNumericHostSuffix(host) {
		// Browsers parse a numeric final label as IPv4, even when the input
		// is malformed. Only canonical IPs accepted above can be shared with
		// the browser; do not publish a numeric spelling as a DNS hostname.
		return "", false
	}
	return origin.String(), true
}

// hasNumericHostSuffix implements the WHATWG ends-in-a-number check on an
// ASCII hostname whose trailing dots have already been removed.
func hasNumericHostSuffix(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	return len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') && strings.Trim(last[2:], "0123456789abcdefABCDEF") == ""
}
