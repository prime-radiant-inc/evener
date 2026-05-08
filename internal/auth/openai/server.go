package openai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// CallbackResult contains the authorization callback parameters returned by OpenAI.
type CallbackResult struct {
	Code  string
	State string
}

// CallbackServer listens for a single localhost OAuth callback.
type CallbackServer struct {
	cfg           Config
	expectedState string
	port          int
	server        *http.Server
	listener      net.Listener
	result        chan callbackResult
	closeOnce     sync.Once
	resultOnce    sync.Once
}

type callbackResult struct {
	result CallbackResult
	err    error
}

// StartCallbackServer starts a localhost listener on port. Use port 0 to ask the OS for a free port.
func StartCallbackServer(cfg Config, port int, expectedState string) (*CallbackServer, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen for OpenAI callback: %w", err)
	}

	actualPort, err := listenerPort(listener.Addr())
	if err != nil {
		listener.Close()
		return nil, err
	}

	callbackServer := &CallbackServer{
		cfg:           cfg,
		expectedState: expectedState,
		port:          actualPort,
		listener:      listener,
		result:        make(chan callbackResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.redirectPath(), callbackServer.handleCallback)
	callbackServer.server = &http.Server{Handler: mux}

	go func() {
		err := callbackServer.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			callbackServer.complete(callbackResult{err: fmt.Errorf("serve OpenAI callback: %w", err)})
		}
	}()

	return callbackServer, nil
}

// RedirectURI returns the localhost redirect URI for the active listener.
func (s *CallbackServer) RedirectURI() string {
	return s.cfg.RedirectURI(s.port)
}

// Wait returns the first valid callback result, callback error, context cancellation, or callback timeout.
func (s *CallbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout := s.cfg.callbackTimeout(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	select {
	case result := <-s.result:
		s.Close()
		return result.result, result.err
	case <-ctx.Done():
		s.Close()
		return CallbackResult{}, ctx.Err()
	}
}

// Close shuts down the callback listener. It is safe to call multiple times.
func (s *CallbackServer) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		closeErr = s.server.Shutdown(ctx)
		if errors.Is(closeErr, http.ErrServerClosed) {
			closeErr = nil
		}
	})
	return closeErr
}

func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code, state, err := parseCallbackRequest(r)
	if err == nil {
		err = ValidateState(s.expectedState, state)
	}

	if err != nil {
		http.Error(w, "OpenAI authentication failed. You can close this window.", http.StatusBadRequest)
		s.complete(callbackResult{err: err})
		go s.Close()
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OpenAI authentication was successful. You can close this window."))
	s.complete(callbackResult{result: CallbackResult{Code: code, State: state}})
	go s.Close()
}

func parseCallbackRequest(r *http.Request) (string, string, error) {
	redirectURL := &url.URL{
		Scheme: "http",
		Host:   r.Host,
		Path:   r.URL.Path,
	}
	redirectURL.RawQuery = r.URL.RawQuery
	return ParseRedirectURL(redirectURL.String())
}

func (s *CallbackServer) complete(result callbackResult) {
	s.resultOnce.Do(func() {
		s.result <- result
	})
}

func listenerPort(addr net.Addr) (int, error) {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("OpenAI callback listener address is %T, want TCP address", addr)
	}
	return tcpAddr.Port, nil
}

func (c Config) callbackTimeout() time.Duration {
	if c.CallbackTimeout == 0 {
		return DefaultConfig().CallbackTimeout
	}
	return c.CallbackTimeout
}
