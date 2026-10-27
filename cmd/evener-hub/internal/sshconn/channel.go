package sshconn

import (
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Channel is one owned SSH channel plus the AppWire client over it.
type Channel struct {
	host hostreg.Host
	// reg is the registration this channel was published for: the registry
	// entry the caller validated under the host lock and passed to
	// publishChannel, captured before ensureOnce folded this Manager's
	// resolved executable path (applyResolvedTarget) into the copy of the host
	// it dialed. Identity pairing (MatchesRegistration) compares reg, not host:
	// a host that configured no evener_path never carries the resolved path in
	// the registry, so comparing host refused the common case. It is written
	// once, under the host lock at publish and before the channel is visible
	// through the map, and never mutated.
	reg   hostreg.Host
	facts Preflight
	// handshake is the InitializeResponse the attach's own initialize captured.
	// It is published once, before the channel is visible through the map, and
	// never mutated, so the attached-only lookups can read it without a lock.
	handshake appwire.InitializeResponse
	stdio     Stdio
	transport *appwire.StreamTransport
	client    *appwire.Client

	lost      chan struct{}
	lostOnce  sync.Once
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	stopped   chan struct{}
	// supervised reports whether a live reconnect loop owns this channel: set
	// by startSupervise when the loop registers, cleared by the loop as it
	// exits. An attach-handoff reads it to avoid starting a second loop for a
	// channel that already has one.
	supervised atomic.Bool
}

// Client returns the initialized AppWire client over this channel.
func (c *Channel) Client() *appwire.Client { return c.client }

// Transport returns the StreamTransport carrying AppWire over ssh stdio.
func (c *Channel) Transport() appwire.Transport { return c.transport }

// Preflight returns the host facts learned before attaching: GOOS/GOARCH,
// HOME, and the resolved config/state roots. The value owns its slice, so a
// caller cannot reach back into the channel's state through it.
func (c *Channel) Preflight() Preflight {
	pf := c.facts
	pf.LaunchFlags = slices.Clone(c.facts.LaunchFlags)
	return pf
}

// Handshake returns the InitializeResponse captured when this channel attached:
// ProtocolVersion, ServerInfo, SourceID, and the peer's advertised Features.
// appwire.Client keeps its own Features copy privately, so attach reads
// ProtocolVersion/SourceID/Features here (reached by host name through
// Manager.ChannelIfAttached and the remoteHostHandshakeForChannel closure, not
// through Manager.HandshakeIfAttached) rather than re-running the
// connection-scoped initialize. ServerInfo is carried but not consumed: its
// Version is the hub's package constant, not its build identity, so HubVersion
// stays preflight-owned.
func (c *Channel) Handshake() appwire.InitializeResponse { return c.handshake }

// Host returns the registry entry this channel was built from. The value owns
// its slice, mirroring Preflight and hostreg's own copies.
func (c *Channel) Host() hostreg.Host {
	h := c.host
	h.Roots = slices.Clone(c.host.Roots)
	return h
}

// MatchesRegistration reports whether entry is the registration this channel
// was published for: content and generation both, the same predicate
// hostreg.SameRegistration wraps. It compares the registration captured at
// publish (reg), not the host the bridge was dialed with (host): that copy
// carries the executable path this Manager resolved for a host that configured
// no evener_path, which the registry entry never does, so comparing it refused
// the common case. It compares in place, so a caller pairing a live channel
// with the entry it renders — and only comparing — need not clone the host and
// its roots.
func (c *Channel) MatchesRegistration(entry hostreg.Host) bool {
	return hostreg.SameRegistration(c.reg, entry)
}

// Close kills the ssh child and closes the stream. It is idempotent and
// terminal.
func (c *Channel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.transport != nil {
			if err := c.transport.Close(); err != nil {
				c.closeErr = err
			}
		}
		if c.stdio != nil {
			if err := c.stdio.Kill(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
			if c.stopped != nil {
				<-c.stopped
			}
		}
		c.markLost()
	})
	return c.closeErr
}

func (c *Channel) markLost() {
	c.lostOnce.Do(func() { close(c.lost) })
}

func (c *Channel) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// isLost reports whether the link has dropped. The channel is unusable from that
// moment on, even though Close may not have run yet.
func (c *Channel) isLost() bool {
	select {
	case <-c.lost:
		return true
	default:
		return false
	}
}

// stdioReadWriter presents a child's stdin/stdout as the single
// io.ReadWriteCloser StreamTransport needs. Nothing else may touch these pipes:
// the bridge owns stdout for frames (component 02's hard rule).
type stdioReadWriter struct {
	in  io.WriteCloser
	out io.ReadCloser
}

func (s *stdioReadWriter) Read(p []byte) (int, error) { return s.out.Read(p) }

func (s *stdioReadWriter) Write(p []byte) (int, error) { return s.in.Write(p) }

func (s *stdioReadWriter) Close() error {
	errIn := s.in.Close()
	errOut := s.out.Close()
	// These pipes are closed from two directions — Channel.Close and the child
	// exiting on its own — so an already-closed pipe is expected, not a teardown
	// failure worth reporting to Manager.Close's caller. os.File reports a repeat
	// close as os.ErrClosed; a pipe reports io.ErrClosedPipe.
	if errIn != nil && !errors.Is(errIn, os.ErrClosed) && !errors.Is(errIn, io.ErrClosedPipe) {
		return errIn
	}
	if errOut != nil && !errors.Is(errOut, os.ErrClosed) && !errors.Is(errOut, io.ErrClosedPipe) {
		return errOut
	}
	return nil
}
