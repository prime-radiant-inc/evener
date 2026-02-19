package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/server"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	model := fs.String("model", "", "LLM model identifier")
	provider := fs.String("provider", "", "LLM provider (openai, anthropic, google)")
	workDir := fs.String("dir", "", "working directory")
	stateDir := fs.String("state-dir", "", "override runtime state directory")
	resume := fs.String("resume", "", "resume a previous session by ID")
	resumeLast := fs.Bool("resume-last", false, "resume the most recent session")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf serve [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Start serf as an HTTP server with REST+SSE API.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve working directory.
	wd := *workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	// Resolve state directory.
	sd := *stateDir
	if sd == "" {
		originURL := cmdutil.GitOriginURLFromDir(wd)
		sd = agent.RuntimeDir(originURL, wd, "")
	}

	// Resolve provider and model (flag > env var).
	prov, err := cmdutil.ResolveProvider(*provider)
	if err != nil {
		return err
	}
	mod, err := cmdutil.ResolveModel(*model)
	if err != nil {
		return err
	}

	// Create LLM client and session.
	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client: %w", err)
	}
	profile, err := cmdutil.SelectProfile(prov, mod)
	if err != nil {
		return err
	}
	env := agent.NewLocalExecutionEnvironment(wd)

	var sess *agent.Session
	if *resume != "" || *resumeLast {
		snap, snapErr := cmdutil.ResolveSnapshot(sd, *resume, *resumeLast)
		if snapErr != nil {
			return snapErr
		}
		sess, err = agent.RestoreSession(client, profile, env, snap, sd)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[serve] resumed session %s (%d turns)\n", snap.ID, snap.TurnCount)
	} else {
		sessionCfg := agent.SessionConfig{StateDir: sd}
		sess, err = agent.NewSession(client, profile, env, sessionCfg)
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}

	// Signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	srv := server.NewServer(server.ServerConfig{})
	srv.SetCompactFunc(sess.Compact)
	srv.SetSteerFunc(sess.Steer)
	srv.SetContextPressureFunc(sess.ContextPressure)
	srv.SetModelFunc(sess.SetModel)
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))

	// Bridge session events to SSE broadcaster.
	go server.Bridge(srv, sess.Events())

	// Input processing loop.
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
				result, processErr := sess.ProcessInput(ctx, text)
				srv.SetProcessing(false)
				srv.SetState("IDLE")
				if processErr != nil {
					fmt.Fprintf(os.Stderr, "[serve] error: %v\n", processErr)
				}
				_ = result
			}
		}
	}()

	// Start HTTP server.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}
	fmt.Fprintf(os.Stderr, "[serve] listening on %s (session %s)\n", listener.Addr(), sess.ID())

	httpSrv := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		httpSrv.Close()
		sess.Close()
	}()

	if err := httpSrv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}
