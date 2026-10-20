package hubendpoint

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// ListenAddr is a validated hub listener address.
type ListenAddr struct {
	host string
	port uint16
}

// ParseListenAddr validates a host:port listener address. Surrounding
// whitespace is ignored, and port zero remains available for ephemeral binds.
func ParseListenAddr(raw string) (ListenAddr, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ListenAddr{}, invalid("empty", "listen_addr")
	}
	if containsControlOrSpace(value) {
		return ListenAddr{}, invalid("control", "listen_addr")
	}

	host, rawPort, err := net.SplitHostPort(value)
	if err != nil {
		code := "host"
		if !strings.ContainsAny(value, "/@?#") && (strings.Count(value, ":") == 0 || strings.HasSuffix(value, "]")) {
			code = "port"
		}
		return ListenAddr{}, invalid(code, "listen_addr")
	}
	port, ok := parsePort(rawPort, true)
	if !ok {
		return ListenAddr{}, invalid("port", "listen_addr")
	}

	if host != "" {
		if strings.ContainsAny(host, "/\\@?#") {
			return ListenAddr{}, invalid("host", "listen_addr")
		}
		if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
			host = addr.String()
		} else {
			host, parseErr = canonicalDNSHost(host)
			if parseErr != nil {
				return ListenAddr{}, invalid("host", "listen_addr")
			}
		}
	}
	return ListenAddr{host: host, port: port}, nil
}

// String returns the canonical host:port listener address.
func (a ListenAddr) String() string {
	return net.JoinHostPort(a.host, strconv.FormatUint(uint64(a.port), 10))
}

func containsControlOrSpace(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func parsePort(raw string, allowZero bool) (uint16, bool) {
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || (!allowZero && value == 0) {
		return 0, false
	}
	return uint16(value), true
}
