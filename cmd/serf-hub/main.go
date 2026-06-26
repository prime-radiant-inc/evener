// Command serf-hub is the web orchestrator for serf serve daemons.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hostlock"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubedge"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/binresolve"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"

	// Side-effect imports register provider adapters. These are the same
	// adapters `serf serve` uses, so the hub's /api/models reflects what
	// spawning will succeed at — only providers configured in the hub's
	// environment surface in the picker.
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/kimi_anthropic"
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
		fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
		printHubEnvVars(os.Stderr)
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
	if err := cmdutil.EnsureUserConfigDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1)
	}

	// flock to ensure single hub per host.
	home, _ := os.UserHomeDir()
	lockPath := filepath.Join(home, ".serf", "hub.lock")
	release, err := hostlock.AcquireLock(lockPath)
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
		stateGlob = DefaultStateGlob()
	}
	pastIndexDB := cfg.PastIndexDB
	if pastIndexDB == "" {
		pastIndexDB = DefaultPastIndexDBPath()
	}

	// Roster + past index
	prober := &hubcore.StatusProber{Timeout: 500 * time.Millisecond}
	roster := hubcore.NewRoster(runDir, prober)

	past := hubcore.NewPastIndexWithDB(stateGlob, pastIndexDB)
	if err := past.Rebuild(); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] past index rebuild: %v\n", err)
	}
	archive := hubcore.NewArchiveStore(pastIndexDB)

	// Spawner
	hubToken, err := newHubToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1) //nolint:gocritic // exitAfterDefer: the deferred release() only drops a flock the kernel frees on process exit
	}
	hubStateRoot := cfg.HubStateRoot
	authToken, err := hubedge.LoadOrCreateAuthToken(hubStateRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] auth token: %v\n", err)
		os.Exit(1)
	}
	credsStore, err := credentials.LoadStore(filepath.Join(hubStateRoot, "credentials.toml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] credentials store: %v\n", err)
		os.Exit(1)
	}
	providersConfigPath := envvars.SERFProvidersConfig.Getenv()
	if providersConfigPath == "" {
		providersConfigPath = filepath.Join(hubStateRoot, "providers.toml")
	}
	var loadedProviderConfig *providercfg.Config
	if pcfg, exists, pcfgErr := providercfg.LoadFile(providersConfigPath); pcfgErr != nil {
		fmt.Fprintf(os.Stderr, "[hub] providers config: %v\n", pcfgErr)
		os.Exit(1)
	} else if exists {
		loadedProviderConfig = &pcfg
	} else {
		// File absent — materialize a descriptors-only providers.toml from the
		// environment so the hub has a single source of truth and spawned
		// children load the same file via SERF_PROVIDERS_CONFIG.
		materialized, matErr := cmdutil.MaterializeProvidersConfig(providersConfigPath)
		if matErr != nil {
			fmt.Fprintf(os.Stderr, "[hub] materialize providers config: %v\n", matErr)
			os.Exit(1)
		}
		loadedProviderConfig = &materialized
		fmt.Fprintf(os.Stderr, "[hub] materialized %s\n", providersConfigPath)
	}
	resolvedSerfBinary := resolveSerfBinaryPath(*serfBinary, currentExecutable(), exec.LookPath)
	if *serfBinary == "" && resolvedSerfBinary != "" && resolvedSerfBinary != "serf" {
		fmt.Fprintf(os.Stderr, "[hub] resolved serf at %s\n", resolvedSerfBinary)
	}
	spawner := &HubSpawner{
		Cfg:                 cfg,
		SerfBinary:          resolvedSerfBinary,
		RunDir:              runDir,
		HubToken:            hubToken,
		Creds:               credsStore,
		StateRoot:           hubStateRoot,
		ProvidersConfigPath: providersConfigPath,
	}
	var codexLauncher *codexlaunch.CodexLauncher
	if len(cfg.CodexLaunches) > 0 {
		codexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
	}

	// stateDir is the parent of the projects/ directory; used for ForkSession
	// as a fallback when a session's project dir can't be found in the past index.
	stateDir := filepath.Dir(filepath.Clean(strings.TrimSuffix(stateGlob, "*")))

	// Keep configured providers available for settings; launch choices come
	// from the Serf harness contract exposed by HubSpawner.
	var models []hubcore.ModelDescriptor
	for _, p := range cfg.Providers {
		for _, m := range p.Models {
			models = append(models, hubcore.ModelDescriptor{Provider: p.Name, Model: m})
		}
	}

	// Web
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:             cfg.Addr,
		AuthToken:           authToken,
		HubStateRoot:        cfg.HubStateRoot,
		RunDir:              runDir,
		PastIndexPath:       pastIndexDB,
		Roster:              roster,
		Past:                past,
		Archive:             archive,
		Spawner:             spawner,
		Models:              models,
		PastPerPage:         cfg.PastResultsPerPage,
		StateDir:            stateDir,
		CredsStore:          credsStore,
		ProviderConfig:      loadedProviderConfig,
		ProvidersConfigPath: providersConfigPath,
		CodexSources:        cfg.CodexSources,
		CodexLaunches:       cfg.CodexLaunches,
		CodexLauncher:       codexLauncher,
	})

	// Lifecycle
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Populate the roster before serving so the first sidebar request can't hit
	// an empty roster (the "flash of no sessions" right after a restart). Probes
	// run concurrently, so this is bounded by ~one probe timeout regardless of
	// how many daemons are live.
	roster.Refresh()
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
	// Build a usable auth URL. If the bind addr is 0.0.0.0 or ::, replace
	// it with a hostname the operator can reach the hub at.
	authHost := cfg.Addr
	if strings.HasPrefix(authHost, "0.0.0.0:") || strings.HasPrefix(authHost, "[::]:") {
		port := authHost[strings.LastIndex(authHost, ":"):]
		host, _ := os.Hostname()
		if host == "" {
			host = "localhost"
		}
		authHost = host + port
	}
	fmt.Fprintf(os.Stderr, "[hub] auth URL (visit once per browser): http://%s/auth?token=%s\n", authHost, authToken)
	fmt.Fprintf(os.Stderr, "[hub] auth token also at %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1)
	}
}

func printHubEnvVars(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range []envvars.Var{
		envvars.SERFProvidersConfig,
		envvars.SERFStateDir,
		envvars.SERFHubEditorURLTemplate,
		envvars.OpenAIAPIKey,
		envvars.AnthropicAPIKey,
		envvars.GeminiAPIKey,
		envvars.GoogleAPIKey,
		envvars.OpenRouterAPIKey,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	_ = tw.Flush()
}

// currentExecutable returns the path of the running serf-hub binary,
// preferring os.Executable() (always absolute on supported platforms)
// and falling back to os.Args[0]. The absolute path is what
// binresolve.Resolve needs to find a sibling "serf" binary even when
// serf-hub was launched via a relative path like "./serf-hub".
func currentExecutable() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}

// resolveSerfBinaryPath determines which "serf" binary the hub should
// invoke for launch-check + spawning. Resolution order is:
//  1. explicit (--serf flag): always wins.
//  2. sibling next to the running serf-hub binary.
//  3. lookup of "serf" on $PATH.
//
// When none of those succeed, "" is returned so HubSpawner falls back
// to its built-in default of running "serf" — which lets exec.Command
// do its own runtime PATH search (matching pre-kata behaviour).
func resolveSerfBinaryPath(explicit, currentExecutable string, lookPath func(string) (string, error)) string {
	if explicit != "" {
		return explicit
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := binresolve.Resolve("serf", "", currentExecutable, lookPath)
	if err != nil {
		// Neither a sibling nor a PATH lookup succeeded. Fall back to
		// the empty default; HubSpawner will invoke "serf" and let
		// exec.Command surface a friendly error if it is unavailable.
		return ""
	}
	return path
}
