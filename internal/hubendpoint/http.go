package hubendpoint

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// HTTPClientWithBearer scopes a bearer credential to one normalized origin.
// A local record additionally pins every connection to its numeric listener and
// verifies the serving process, without using an ambient proxy or a second socket.
func HTTPClientWithBearer(base *http.Client, token string, origin Origin, local *Record) (*http.Client, error) {
	if origin.String() == "" {
		return nil, errors.New("missing hub origin")
	}
	if base == nil {
		base = &http.Client{}
	}
	if token == "" && local == nil {
		return base, nil
	}
	clone := *base
	inner := base.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	if local != nil {
		if err := validateRecord(*local, local.HubStateRoot); err != nil {
			return nil, err
		}
		if origin.Scheme() != "http" {
			return nil, errors.New("verified local bearer requires HTTP")
		}
		pinned, err := LocalOrigin(local.BoundAddr)
		if err != nil || pinned.Port() != origin.Port() {
			return nil, errors.New("local bearer destination does not match listener")
		}
		defaults := http.DefaultTransport.(*http.Transport)
		transport := defaults.Clone()
		if settings, ok := inner.(*http.Transport); ok && settings != defaults {
			transport = settings.Clone()
		}
		transport.Proxy = nil
		transport.DialTLSContext = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := &net.Dialer{}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(pinned.Hostname(), portString(pinned.Port())))
			if err != nil {
				return nil, err
			}
			if err := VerifyLocalConn(ctx, conn, origin, *local); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
		inner = transport
	}
	clone.Transport = &originBearerTransport{base: inner, token: token, origin: origin.String()}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("authenticated hub redirect refused")
	}
	return &clone, nil
}

func portString(port uint16) string {
	return strconv.FormatUint(uint64(port), 10)
}

type originBearerTransport struct {
	base   http.RoundTripper
	token  string
	origin string
}

func (t *originBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.User != nil || req.URL.Opaque != "" {
		return nil, errors.New("invalid authenticated destination")
	}
	destination, err := ParseOrigin((&url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host}).String())
	if err != nil || destination.String() != t.origin {
		return nil, errors.New("authenticated destination does not match selected origin")
	}
	if t.token == "" {
		return t.base.RoundTrip(req)
	}
	withBearer := req.Clone(req.Context())
	withBearer.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(withBearer)
}
