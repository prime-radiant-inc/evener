// Command serf-hub is the web orchestrator for serf serve daemons.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"primeradiant.com/serf/rendezvous"

	// Side-effect imports register provider adapters. These are the same
	// adapters `serf serve` uses, so the hub's /api/models reflects what
	// spawning will succeed at — only providers configured in the hub's
	// environment surface in the picker.
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
)

const Version = "0.1.0"

func main() {
	configPath := flag.String("config", DefaultConfigPath(), "path to hub.toml")
	addr := flag.String("addr", "", "override hub listen address")
	serfBinary := flag.String("serf", "", "path to serf binary (default: 'serf' on PATH)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf-hub [flags]\n\nMulti-session web orchestrator for serf serve daemons.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] config: %v\n", err)
		os.Exit(1)
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	// flock to ensure single hub per host.
	home, _ := os.UserHomeDir()
	lockPath := filepath.Join(home, ".serf", "hub.lock")
	release, err := AcquireLock(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1)
	}
	defer release()

	// Resolve runtime paths.
	runDir := cfg.RunDir
	if runDir == "" {
		runDir = rendezvous.DefaultDir()
	}
	stateGlob := cfg.StateGlob
	if stateGlob == "" {
		stateGlob = filepath.Join(home, ".local", "state", "serf", "projects", "*")
	}

	// Roster + past index
	prober := &StatusProber{Timeout: 500 * time.Millisecond}
	roster := NewRoster(runDir, prober)

	past := NewPastIndex(stateGlob)
	if err := past.Rebuild(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] past index rebuild: %v\n", err)
	}

	// Spawner
	spawner := &HubSpawner{
		Cfg:        cfg,
		SerfBinary: *serfBinary,
		RunDir:     runDir,
	}
	var codexLauncher *CodexLauncher
	if len(cfg.CodexLaunches) > 0 {
		codexLauncher = NewCodexLauncher(cfg.CodexLaunches)
	}

	// stateDir is the parent of the projects/ directory; used for ForkSession
	// as a fallback when a session's project dir can't be found in the past index.
	stateDir := filepath.Dir(filepath.Clean(strings.TrimSuffix(stateGlob, "*")))

	// Build model list from provider config for the spawn chip.
	var models []modelDescriptor
	for _, p := range cfg.Providers {
		for _, m := range p.Models {
			models = append(models, modelDescriptor{Provider: p.Name, Model: m})
		}
	}

	// Web
	web := NewWebServer(WebConfig{
		HubAddr:       cfg.Addr,
		RunDir:        runDir,
		Roster:        roster,
		Past:          past,
		Spawner:       spawner,
		Models:        models,
		PastPerPage:   cfg.PastResultsPerPage,
		StateDir:      stateDir,
		CodexSources:  cfg.CodexSources,
		CodexLaunches: cfg.CodexLaunches,
		CodexLauncher: codexLauncher,
	})

	// Lifecycle
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := roster.Watch(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[hub] roster watch: %v\n", err)
		}
	}()

	go func() {
		ticker := time.NewTicker(cfg.PastIndexRebuild)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = past.Rebuild()
			}
		}
	}()

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: web.Handler(),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(shutdownCtx)
		if codexLauncher != nil {
			_ = codexLauncher.Shutdown(shutdownCtx)
		}
	}()

	fmt.Fprintf(os.Stderr, "[hub] serf-hub %s listening on %s (run_dir=%s)\n", Version, cfg.Addr, runDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1)
	}
}
