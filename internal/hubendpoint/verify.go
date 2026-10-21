package hubendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var ErrIdentityMismatch = errors.New("local health identity mismatch")

// VerifiedHealth is the token-free identity and build projection of /api/health.
type VerifiedHealth struct {
	BootID        string    `json:"boot_id"`
	HubAddr       string    `json:"hub_addr"`
	StartedAt     time.Time `json:"started_at"`
	Version       string    `json:"version"`
	BackendGitSha string    `json:"backend_git_sha"`
}

// VerifyLocalConn verifies the published instance identity without a token on the
// supplied connection. It is not proof that the answering process holds the token.
// A successful verification drains health and leaves the exact connection reusable.
// The caller must close a connection after any failure.
func VerifyLocalConn(ctx context.Context, conn net.Conn, origin Origin, expected Record) error {
	_, err := VerifiedLocalHealth(ctx, conn, origin, expected)
	return err
}

// VerifiedLocalHealth verifies the published instance identity on the supplied
// connection and returns its reported version. It is not proof of token possession.
func VerifiedLocalHealth(ctx context.Context, conn net.Conn, origin Origin, expected Record) (VerifiedHealth, error) {
	if ctx.Err() != nil {
		return VerifiedHealth{}, ctx.Err()
	}
	bound, err := ParseListenAddr(expected.BoundAddr)
	if err != nil || bound.port == 0 || origin.Scheme() != "http" || origin.Port() != bound.port {
		return VerifiedHealth{}, errors.New("invalid local health target")
	}
	remote, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	if err != nil || remote.Port() != bound.port || !listenerAccepts(bound, remote.Addr()) || !localAddress(remote.Addr()) {
		return VerifiedHealth{}, errors.New("connection does not match local listener")
	}
	// A DNS origin such as localhost is eligible only through the numeric,
	// locally owned socket above. Numeric origins must also name that socket.
	if named, err := netip.ParseAddr(origin.Hostname()); err == nil && named.Unmap() != remote.Addr().Unmap() {
		return VerifiedHealth{}, errors.New("origin does not match connection")
	}
	deadline := time.Now().Add(2 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return VerifiedHealth{}, err
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()); close(cancelDone) })
	stopped := false
	finishCancellation := func() {
		if stopped {
			return
		}
		if !stop() {
			<-cancelDone
		}
		stopped = true
	}
	defer finishCancellation()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/api/health", nil)
	if err != nil {
		return VerifiedHealth{}, err
	}
	req.Header.Set("Accept", "application/json")
	if err := req.Write(conn); err != nil {
		return VerifiedHealth{}, err
	}
	limited := &io.LimitedReader{R: conn, N: maxEndpointBytes + 1}
	buffered := bufio.NewReader(limited)
	resp, err := http.ReadResponse(buffered, req)
	if err != nil {
		return VerifiedHealth{}, err
	}
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return VerifiedHealth{}, readErr
	}
	if limited.N == 0 || len(data) > maxEndpointBytes {
		return VerifiedHealth{}, errors.New("local health response exceeds size limit")
	}
	closeErr := resp.Body.Close()
	if closeErr != nil {
		return VerifiedHealth{}, closeErr
	}
	if buffered.Buffered() != 0 || resp.StatusCode != http.StatusOK || resp.Close || resp.ProtoMajor != 1 {
		return VerifiedHealth{}, errors.New("invalid local health response")
	}
	var health VerifiedHealth
	if err := json.Unmarshal(data, &health); err != nil {
		return VerifiedHealth{}, err
	}
	if health.BootID == "" || health.BootID != expected.BootID || health.HubAddr != expected.BoundAddr || !health.StartedAt.Equal(expected.StartedAt) {
		return VerifiedHealth{}, ErrIdentityMismatch
	}
	finishCancellation()
	if err := ctx.Err(); err != nil {
		return VerifiedHealth{}, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return VerifiedHealth{}, err
	}
	return health, nil
}

// LocalTarget reports whether the numeric origin currently belongs to this host.
// It does not turn DNS resolution into evidence of local ownership.
func LocalTarget(origin Origin) bool {
	addr, err := netip.ParseAddr(origin.Hostname())
	return err == nil && localAddress(addr)
}

func originIsLoopback(origin Origin) bool {
	addr, err := netip.ParseAddr(origin.Hostname())
	return err == nil && addr.Unmap().IsLoopback()
}

func listenerAccepts(bound ListenAddr, remote netip.Addr) bool {
	addr, err := netip.ParseAddr(bound.host)
	if err != nil {
		return false
	}
	if addr.IsUnspecified() {
		return addr.Is4() == remote.Is4()
	}
	return addr.Unmap() == remote.Unmap()
}
func localAddress(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	if addr.Unmap().IsLoopback() {
		return true
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, item := range ifaces {
		if ipnet, ok := item.(*net.IPNet); ok {
			if assigned, ok := netip.AddrFromSlice(ipnet.IP); ok && assigned.Unmap() == addr.Unmap() {
				return true
			}
		}
	}
	return false
}
