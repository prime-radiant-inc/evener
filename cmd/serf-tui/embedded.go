package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/server"
)

// embeddedConfig holds options for the embedded server.
type embeddedConfig struct {
	provider string
	model    string
	workDir  string
	stateDir string
}

// embeddedServer wraps an in-process agent session and HTTP server.
type embeddedServer struct {
	addr     string
	srv      *server.Server
	httpSrv  *http.Server
	sess     *agent.Session
	listener net.Listener
	cancel   context.CancelFunc
}

// startEmbedded creates an agent session and HTTP server in-process,
// listening on a random localhost port. Returns the server and its address.
func startEmbedded(ctx context.Context, cfg embeddedConfig) (*embeddedServer, error) {
	wd := cfg.workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	sd := cfg.stateDir
	if sd == "" {
		originURL := cmdutil.GitOriginURLFromDir(wd)
		sd = agent.RuntimeDir(originURL, wd, "")
	}

	prov, err := cmdutil.ResolveProvider(cfg.provider)
	if err != nil {
		return nil, err
	}
	mod, err := cmdutil.ResolveModel(cfg.model)
	if err != nil {
		return nil, err
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		return nil, fmt.Errorf("LLM client: %w", err)
	}

	profile, err := cmdutil.SelectProfile(prov, mod)
	if err != nil {
		return nil, err
	}

	env := agent.NewLocalExecutionEnvironment(wd)
	sessionCfg := agent.SessionConfig{StateDir: sd}
	sess, err := agent.NewSession(client, profile, env, sessionCfg)
	if err != nil {
		return nil, fmt.Errorf("session creation: %w", err)
	}

	srv := server.NewServer(server.ServerConfig{})

	// Bridge session events to SSE broadcaster.
	go server.Bridge(srv, sess.Events())

	// Input processing loop.
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case text, ok := <-srv.InputCh():
				if !ok {
					return
				}
				srv.SetProcessing(true)
				srv.SetState("PROCESSING")
				_, processErr := sess.ProcessInput(ctx, text)
				srv.SetProcessing(false)
				srv.SetState("IDLE")
				if processErr != nil {
					fmt.Fprintf(os.Stderr, "[embedded] error: %v\n", processErr)
				}
			}
		}
	}()

	// Listen on random localhost port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		sess.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}

	httpSrv := &http.Server{Handler: srv}
	go httpSrv.Serve(listener) //nolint:errcheck

	return &embeddedServer{
		addr:     listener.Addr().String(),
		srv:      srv,
		httpSrv:  httpSrv,
		sess:     sess,
		listener: listener,
		cancel:   cancel,
	}, nil
}

// Close shuts down the embedded server and session.
func (e *embeddedServer) Close() {
	e.cancel()
	e.httpSrv.Close()
	e.sess.Close()
}
