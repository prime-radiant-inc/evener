package hubendpoint

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

var hostnameProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.CheckHyphens(false))

// Origin is a canonical HTTP or HTTPS hub origin.
type Origin struct {
	scheme   string
	hostname string
	port     uint16
	value    string
}

// ParseOrigin validates and canonicalizes an HTTP(S) origin.
func ParseOrigin(raw string) (Origin, error) {
	if raw == "" {
		return Origin{}, invalid("empty", "origin")
	}
	if containsControlOrSpace(raw) {
		return Origin{}, invalid("control", "origin")
	}
	if strings.ContainsRune(raw, '\\') {
		return Origin{}, invalid("path", "path")
	}

	schemeEnd := strings.Index(raw, "://")
	if schemeEnd <= 0 {
		return Origin{}, invalid("scheme", "scheme")
	}
	scheme := strings.ToLower(raw[:schemeEnd])
	if scheme != "http" && scheme != "https" {
		return Origin{}, invalid("scheme", "scheme")
	}

	remainder := raw[schemeEnd+3:]
	if strings.ContainsRune(remainder, '?') {
		return Origin{}, invalid("query", "query")
	}
	if strings.ContainsRune(remainder, '#') {
		return Origin{}, invalid("fragment", "fragment")
	}

	authority := remainder
	path := ""
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		authority = remainder[:slash]
		path = remainder[slash:]
	}
	if path != "" && path != "/" {
		return Origin{}, invalid("path", "path")
	}
	if authority == "" {
		return Origin{}, invalid("host", "host")
	}
	if strings.ContainsRune(authority, '@') {
		return Origin{}, invalid("userinfo", "userinfo")
	}

	_, rawPort, explicitPort, err := splitOriginAuthority(authority)
	if err != nil {
		return Origin{}, err
	}
	var port uint16
	if explicitPort {
		var ok bool
		port, ok = parsePort(rawPort, false)
		if !ok {
			return Origin{}, invalid("port", "port")
		}
	} else if scheme == "http" {
		port = 80
	} else {
		port = 443
	}

	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil {
		return Origin{}, invalid("host", "host")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return Origin{}, invalid("host", "host")
	}
	hostname, err = canonicalOriginHost(hostname)
	if err != nil {
		return Origin{}, err
	}
	return newOrigin(scheme, hostname, port), nil
}

// LocalOrigin derives a local HTTP origin from an actual bound numeric address.
func LocalOrigin(boundAddr string) (Origin, error) {
	listenAddr, err := ParseListenAddr(boundAddr)
	if err != nil {
		return Origin{}, err
	}
	if listenAddr.port == 0 {
		return Origin{}, invalid("port", "port")
	}
	if listenAddr.host == "" {
		return Origin{}, invalid("host", "host")
	}

	addr, parseErr := netip.ParseAddr(listenAddr.host)
	if parseErr != nil {
		return Origin{}, invalid("host", "host")
	}
	if addr.Zone() != "" {
		return Origin{}, invalid("scoped_host", "host")
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		if addr.Is4() {
			addr = netip.MustParseAddr("127.0.0.1")
		} else {
			addr = netip.IPv6Loopback()
		}
	}
	return newOrigin("http", addr.String(), listenAddr.port), nil
}

// String returns the canonical HTTP(S) origin.
func (o Origin) String() string { return o.value }

// RPCURL returns the origin's AppWire WebSocket endpoint.
func (o Origin) RPCURL() string {
	if o.value == "" {
		return ""
	}
	rpcScheme := "ws"
	if o.scheme == "https" {
		rpcScheme = "wss"
	}
	return rpcScheme + "://" + originAuthority(o.hostname, o.port, o.scheme) + "/rpc"
}

// Hostname returns the canonical hostname without brackets.
func (o Origin) Hostname() string { return o.hostname }

// Port returns the effective origin port, including the scheme default.
func (o Origin) Port() uint16 { return o.port }

// Scheme returns http or https.
func (o Origin) Scheme() string { return o.scheme }

func splitOriginAuthority(authority string) (host, port string, explicitPort bool, err error) {
	if strings.HasPrefix(authority, "[") {
		closing := strings.IndexByte(authority, ']')
		if closing < 0 {
			return "", "", false, invalid("host", "host")
		}
		if closing == len(authority)-1 {
			return authority[1:closing], "", false, nil
		}
		if authority[closing+1] != ':' {
			return "", "", false, invalid("host", "host")
		}
		host, port, splitErr := net.SplitHostPort(authority)
		if splitErr != nil {
			return "", "", false, invalid("port", "port")
		}
		return host, port, true, nil
	}

	switch strings.Count(authority, ":") {
	case 0:
		return authority, "", false, nil
	case 1:
		host, port, splitErr := net.SplitHostPort(authority)
		if splitErr != nil {
			return "", "", false, invalid("port", "port")
		}
		return host, port, true, nil
	default:
		return "", "", false, invalid("host", "host")
	}
}

func canonicalOriginHost(host string) (string, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", invalid("scoped_host", "host")
		}
		if addr.IsUnspecified() || addr.Unmap().IsUnspecified() {
			return "", invalid("wildcard", "host")
		}
		return canonicalIPLiteral(addr), nil
	}

	ascii, err := canonicalDNSHost(host)
	if err != nil {
		return "", invalid("host", "host")
	}
	numericHost := strings.TrimSuffix(ascii, ".")
	if addr, parseErr := netip.ParseAddr(numericHost); parseErr == nil && addr.IsUnspecified() {
		return "", invalid("wildcard", "host")
	}
	if hasNumericHostSuffix(ascii) {
		return "", invalid("host", "host")
	}
	return ascii, nil
}

func canonicalIPLiteral(addr netip.Addr) string {
	if !addr.Is4In6() {
		return addr.String()
	}
	bytes := addr.As16()
	high := uint16(bytes[12])<<8 | uint16(bytes[13])
	low := uint16(bytes[14])<<8 | uint16(bytes[15])
	return "::ffff:" + strconv.FormatUint(uint64(high), 16) + ":" + strconv.FormatUint(uint64(low), 16)
}

func canonicalDNSHost(host string) (string, error) {
	ascii, err := hostnameProfile.ToASCII(host)
	if err != nil || ascii == "" {
		return "", invalid("host", "host")
	}
	return strings.ToLower(ascii), nil
}

// hasNumericHostSuffix implements the WHATWG ends-in-a-number check. Such
// spellings are not canonical IP literals and browsers can reinterpret them.
func hasNumericHostSuffix(host string) bool {
	host = strings.TrimSuffix(host, ".")
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	return len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') && strings.Trim(last[2:], "0123456789abcdefABCDEF") == ""
}

func newOrigin(scheme, hostname string, port uint16) Origin {
	authority := originAuthority(hostname, port, scheme)
	return Origin{
		scheme:   scheme,
		hostname: hostname,
		port:     port,
		value:    scheme + "://" + authority,
	}
}

func originAuthority(hostname string, port uint16, scheme string) string {
	host := hostname
	if strings.ContainsRune(hostname, ':') {
		host = "[" + hostname + "]"
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return host
	}
	return net.JoinHostPort(hostname, strconv.FormatUint(uint64(port), 10))
}
