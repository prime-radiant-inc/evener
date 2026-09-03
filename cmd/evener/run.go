package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
)

type runConfig struct {
	prompt                    string
	model                     string
	fastCheapModel            string // --fast-cheap-model override for auxiliary side calls
	visionModel               string // --vision-model override for the image-description side-channel
	workDir                   string
	stateDir                  string   // --state-dir override
	systemPrompt              string   // --system-prompt file path
	systemPromptAppend        []string // --system-prompt-append file paths
	maxRounds                 int      // --max-rounds (-1=default, 0=unlimited, >0=limit)
	maxSubagentDepth          int      // --max-subagent-depth (-1=default)
	maxConcurrentDelegates    int      // --max-concurrent-delegates (-1=default)
	maxRetainedTerminal       int      // --max-retained-terminal (-1=default)
	shareTaskStore            bool     // --share-task-store
	resultToolName            string   // --result-tool-name override
	reasoningEffort           string   // --reasoning-effort override (or EVENER_REASONING_EFFORT)
	contextStrategy           string   // --context-strategy
	exportATIF                string   // --export-atif path
	exportATIFProviderHandles string   // --export-atif-provider-handles
	outputSchema              string   // --output-schema: raw JSON schema applied to communicate.output
	verbose                   bool
	noProjectPrompts          bool
	agentName                 string // --agent persona name (default: default)
	stdout                    io.Writer
	stderr                    io.Writer

	skillsDirs                  []string      // extra skill directories
	mcpServers                  []string      // --mcp inline specs
	mcpConfigs                  []string      // --mcp-config file paths
	pluginDirs                  []string      // --plugin-dir directories
	enabledPlugins              *[]string     // --enabled-plugins selection; nil means omitted
	noDefaultMarketplaces       bool          // --no-default-marketplaces
	systemPromptAsUser          bool          // --system-prompt-as-user
	openAIResponsesContinuation string        // --openai-responses-continuation
	runTimeout                  time.Duration // --timeout; zero disables
	sandboxMode                 string        // --sandbox mode name (default "off")
	sandboxNet                  string        // --sandbox-net on|off

	// Resume options.
	resume       string // session ID to resume
	resumeWith   string // session ID whose context to reuse with a new prompt
	resumeLast   bool   // resume the most recent session
	listSessions bool   // print saved sessions and exit
}

// runLoadClient is the injectable hook for tests. Production code calls
// cmdutil.LoadClient; tests may replace this to drive run() with a scripted
// provider while exercising the real CLI/session/tool plumbing.
var runLoadClient = cmdutil.LoadClient

var (
	runGetwd                = os.Getwd
	runEnsureUserConfigDirs = cmdutil.EnsureUserConfigDirs
	runSeedMarketplaces     = func(ctx context.Context) error {
		_, err := plugins.NewManager("").SeedDefaultMarketplaces(ctx)
		return err
	}
	runAttachAPILogger  = cmdutil.AttachSessionAPILogger
	runNewSession       = agent.NewSession
	runRestoreSession   = agent.RestoreSessionFromMetaWithConfig
	runProvisionSandbox = provisionSandbox
	runSandboxLine      = sandboxEnforcementLine
	runProcessInput     = func(sess *agent.Session, ctx context.Context, prompt string) (string, error) {
		return sess.ProcessInput(ctx, prompt, nil)
	}
	runDrainJobTree = func(sess *agent.Session, ctx context.Context) (string, error) {
		return sess.DrainJobTree(ctx)
	}
	runResolvePlugins = func(ctx context.Context, explicit []string, enabled *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.NewManager("").ResolveForLaunch(ctx, explicit, enabled)
	}
)

func run(ctx context.Context, cfg runConfig) error {
	if err := rejectPluginSelectionWithResume(cfg.enabledPlugins, cfg.resume, cfg.resumeLast); err != nil {
		return err
	}
	if cfg.runTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.runTimeout)
		defer cancel()
	}
	ctx = llm.WithRunBudget(ctx)
	if cfg.stdout == nil {
		cfg.stdout = os.Stdout
	}
	if cfg.stderr == nil {
		cfg.stderr = os.Stderr
	}
	if cfg.workDir == "" {
		wd, err := runGetwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
		cfg.workDir = wd
	}
	// Before anything that can create the user config root: the legacy-data
	// guard inside EnsureUserConfigDirs reads an existing root as already
	// migrated, and resolving a requested bundled plugin materializes it under
	// exactly that root. Running the guard second would strand a user's legacy
	// configuration and credentials silently.
	if err := runEnsureUserConfigDirs(); err != nil {
		return err
	}
	resolvedPlugins, err := runResolvePlugins(ctx, cfg.pluginDirs, cfg.enabledPlugins)
	if fatal := fatalLaunchPluginError(err, cfg.enabledPlugins); fatal != nil {
		return fatal
	}
	if err != nil {
		fmt.Fprintf(cfg.stderr, "warning: listing installed plugins: %v\n", err) //nolint:errcheck
	}
	renderLaunchPluginDiagnostics(cfg.stderr, resolvedPlugins.Diagnostics)
	if err := resolvedPlugins.ValidateSelection(); err != nil {
		return err
	}
	// --no-default-marketplaces opts out of seeding on this bare-evener path only;
	// serve and plugin subcommands always seed (best-effort, first-run-only).
	if !cfg.noDefaultMarketplaces {
		if err := runSeedMarketplaces(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: seeding default marketplaces: %v\n", err)
		}
	}
	openAIResponsesContinuation := resolveOpenAIResponsesContinuation(cfg.openAIResponsesContinuation, nil)

	// Compute runtime state directory.
	// Priority: --state-dir flag > EVENER_STATE_DIR env > XDG-computed default.
	var project identifier.Project
	stateDir := cfg.stateDir
	if stateDir == "" {
		stateDir = envvars.EVENERStateDir.Getenv()
	}
	if stateDir == "" {
		var err error
		project, stateDir, err = cmdutil.DefaultProjectStateDir(cfg.workDir)
		if err != nil {
			return fmt.Errorf("resolve project state: %w", err)
		}
	}
	// --list-sessions: print and exit.
	if cfg.listSessions {
		return listSessions(cfg, stateDir)
	}

	// Resolve resume target.
	var meta *schema.SessionMeta
	if cfg.resume != "" || cfg.resumeWith != "" || cfg.resumeLast {
		m, err := resolveSessionMeta(cfg, stateDir)
		if err != nil {
			return err
		}
		meta = &m
	}

	// Determine prompt.
	prompt := strings.TrimSpace(cfg.prompt)
	if meta != nil && cfg.resumeWith == "" && prompt == "" {
		// --resume without a prompt: continue with a generic prompt.
		prompt = "Continue where you left off."
	}
	if prompt == "" && meta == nil {
		return errors.New("no prompt provided")
	}

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, envvars.EVENERReasoningEffort.Getenv())
	if err != nil {
		return err
	}

	resumeProvider := ""
	resumeModel := ""
	if meta != nil {
		resumeProvider = meta.ProfileID
		resumeModel = meta.Model
	}
	var modelRef cmdutil.ModelRef
	if meta != nil {
		modelRef, err = cmdutil.ResolveResumeModelRef(cfg.model, envvars.EVENERModel.Getenv(), resumeProvider, resumeModel)
	} else {
		modelRef, err = cmdutil.ResolveModelRef(cfg.model, envvars.EVENERModel.Getenv(), "", "")
	}
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := runLoadClient(stateDir)
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}
	printRegistryNotices(cfg.stderr, client.Registry())
	if err := ctx.Err(); err != nil {
		return err
	}

	reserveSession, closeAPILog, err := runAttachAPILogger(client, stateDir, cfg.stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
	var resumeWithChildID string
	resumeWithRollbackAllowed := false
	resumeWithCommitted := false
	if meta != nil && cfg.resumeWith != "" {
		childConfig := meta.Config.Clone()
		childConfig.PluginDirs = append([]string(nil), resolvedPlugins.SelectedDirs...)
		childID, err := agent.AsideSessionWithConfig(stateDir, meta.ID, childConfig)
		if err != nil {
			return fmt.Errorf("create resume-with session: %w", err)
		}
		resumeWithChildID = childID
		resumeWithRollbackAllowed = true
		defer func() {
			if resumeWithCommitted || !resumeWithRollbackAllowed {
				return
			}
			if err := agent.RemoveSessionArtifacts(stateDir, resumeWithChildID); err != nil {
				fmt.Fprintf(cfg.stderr, "warning: could not roll back resume-with session %s: %v\n", resumeWithChildID, err) //nolint:errcheck
			}
		}()
		if err := reserveSession(resumeWithChildID); err != nil {
			if errors.Is(err, llm.ErrAPILogTargetLocked) {
				resumeWithRollbackAllowed = false
			}
			return err
		}
		childMeta, err := schema.LoadSessionMeta(stateDir, childID)
		if err != nil {
			return fmt.Errorf("load resume-with session: %w", err)
		}
		meta = &childMeta
	}
	if meta != nil && resumeWithChildID == "" {
		if err := reserveSession(meta.ID); err != nil {
			return err
		}
	}

	profile, err := buildInitialProfile(client, modelRef, cfg.outputSchema)
	if err != nil {
		return err
	}
	profile, err = applyFastCheapModel(profile, cfg.fastCheapModel, client)
	if err != nil {
		return err
	}
	visionModel, err := applyVisionModel(profile, cfg.visionModel, client)
	if err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(cfg.workDir)
	// A daemon/session launched outside the developer's shell rc chain (macOS
	// launchd, a GUI app, systemd) inherits a PATH lacking tool directories like
	// /opt/homebrew/bin; the login shell's own PATH wins for spawned commands
	// (kata 31gh). LoginShellPATH probes once per process with a short timeout
	// and never blocks launch — it falls back to "" (inherited PATH unchanged)
	// on any failure.
	env.LoginPATH = execenv.LoginShellPATH()

	var sess *agent.Session
	baseSessionCfg := agent.SessionConfig{
		LifetimeContext:             ctx,
		MaxToolRoundsPerInput:       cmdutil.MaxRoundsToConfig(cfg.maxRounds),
		ShareTasksWithChildren:      cfg.shareTaskStore,
		ResultToolName:              cfg.resultToolName,
		StateDir:                    stateDir,
		AcquireSessionOwnership:     reserveSession,
		Project:                     project,
		SystemPromptFile:            cfg.systemPrompt,
		SystemPromptAppend:          cfg.systemPromptAppend,
		NoProjectPrompts:            cfg.noProjectPrompts,
		AgentName:                   cfg.agentName,
		SkillsDirs:                  cfg.skillsDirs,
		MCPConfigFiles:              cfg.mcpConfigs,
		MCPInline:                   cfg.mcpServers,
		PluginDirs:                  resolvedPlugins.SelectedDirs,
		ContextStrategy:             cfg.contextStrategy,
		ExportATIFPath:              cfg.exportATIF,
		ExportATIFProviderHandles:   cfg.exportATIFProviderHandles,
		VisionModel:                 visionModel,
		NonInteractive:              true,
		TurnEndsProcess:             true,
		SystemPromptAsUser:          cfg.systemPromptAsUser,
		OpenAIResponsesContinuation: openAIResponsesContinuation,
		ResolveProfile:              cmdutil.BuildResolveProfile(client),
	}
	if cfg.maxSubagentDepth >= 0 {
		baseSessionCfg.MaxSubagentDepth = cfg.maxSubagentDepth
	}
	if cfg.maxConcurrentDelegates >= 0 {
		baseSessionCfg.MaxConcurrentDelegateTurns = cfg.maxConcurrentDelegates
	}
	if cfg.maxRetainedTerminal >= 0 {
		baseSessionCfg.MaxRetainedTerminal = cfg.maxRetainedTerminal
	}
	if effort.Set {
		baseSessionCfg.ReasoningEffort = effort.Value
	}
	if err := configureSandbox(&baseSessionCfg, cfg.sandboxMode, cfg.sandboxNet); err != nil {
		return err
	}
	// Engage enforcement for a FRESH session from the flag-set mode. A resume
	// re-provisions the env from the PERSISTED mode inside
	// RestoreSessionFromMetaWithConfig (the immutable-across-restart guarantee), so
	// the flag governs only new sessions here.
	if meta == nil {
		if err := runProvisionSandbox(env, &baseSessionCfg, env.WorkingDirectory()); err != nil {
			return err
		}
	}
	if meta != nil {
		sess, err = runRestoreSession(client, profile, env, *meta, agent.RestoreSessionConfig{
			LifetimeContext:             ctx,
			StateDir:                    stateDir,
			Project:                     project,
			ResolveProfile:              baseSessionCfg.ResolveProfile,
			AcquireSessionOwnership:     reserveSession,
			OwnershipAlreadyAcquired:    true,
			OpenAIResponsesContinuation: openAIResponsesContinuation,
			TurnEndsProcess:             baseSessionCfg.TurnEndsProcess,
		})
		if err != nil {
			// A resume provisions this environment's sandbox from the
			// session's persisted mode inside the restore, and the restore can
			// fail after that with no session built to own the scratch and the
			// flock lease it took.
			env.DisposeSandboxScratch()
			return fmt.Errorf("restore session: %w", err)
		}
		if resumeWithChildID != "" {
			resumeWithCommitted = true
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		if strings.TrimSpace(cfg.model) != "" {
			fmt.Fprintf(cfg.stderr, "[resumed] session %s with model override %s (was %s/%s)\n", meta.ID, modelRef.Qualified(), resumeProvider, resumeModel) //nolint:errcheck
		} else {
			fmt.Fprintf(cfg.stderr, "[resumed] session %s (%d turns)\n", meta.ID, meta.TurnCount) //nolint:errcheck
		}
	} else {
		sess, err = runNewSession(client, profile, env, baseSessionCfg)
		if err != nil {
			// The session that would have owned the scratch was never built.
			env.DisposeSandboxScratch()
			return fmt.Errorf("session creation: %w", err)
		}
	}
	defer sess.Close()

	// One startup line, loudly, states exactly what this host enforces (read from
	// the env's resolved policy so it never overstates). Empty for an unsandboxed
	// session — nothing to announce.
	if line := runSandboxLine(env); line != "" {
		fmt.Fprintln(cfg.stderr, line) //nolint:errcheck
	}

	var done <-chan struct{}
	if cfg.verbose {
		done = drainEventsVerbose(sess.Events(), cfg.stderr)
	} else {
		done = drainEventsHuman(sess.Events(), cfg.stderr)
	}

	result, err := runProcessInput(sess, ctx, prompt)
	if err == nil {
		// Drain every session-owned managed job before Close() SIGKILLs it: keep
		// re-driving the coordinator on job completions until the job tree is
		// terminal. The coordinator's real final answer is produced on the
		// post-completion notification turn, so prefer it over the "waiting on
		// delegate" turn that ended ProcessInput (PRI-2441).
		if drained, derr := runDrainJobTree(sess, ctx); derr != nil {
			err = derr
		} else if drained != "" {
			result = drained
		}
	}
	sess.Close()
	<-done

	if err != nil {
		return err
	}

	fmt.Fprintln(cfg.stdout, result) //nolint:errcheck
	return nil
}

// drainEventsVerbose writes every event as a JSON line (NDJSON) to w.
func drainEventsVerbose(eventCh <-chan events.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for ev := range eventCh {
			enc.Encode(ev) //nolint:errcheck
		}
	}()
	return done
}

// drainEventsHuman writes human-readable status lines to w.
func drainEventsHuman(eventCh <-chan events.SessionEvent, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The most recent answer written to w, so a communicate that merely
		// echoes it is not printed a second time. Events carry no turn ID here,
		// so "most recent" is the closest this surface gets to the projector's
		// turn scoping -- and for the stream this dedupes (assistant text
		// immediately followed by its communicate echo) it is the same contract.
		lastAssistantText := ""
		for ev := range eventCh {
			switch ev.Kind {
			case events.EventSessionStart:
				if d, ok := ev.Data.(events.SessionStartData); ok && d.Model != "" {
					fmt.Fprintf(w, "[model] %s (%s)\n", d.Model, d.Profile) //nolint:errcheck
				}
			case events.EventPromptLoaded:
				if d, ok := ev.Data.(events.PromptLoadedData); ok {
					fmt.Fprintf(w, "[prompt] %s (%dB)\n", d.Label, d.Size) //nolint:errcheck
				}
			case events.EventAssistantTextEnd:
				if d, ok := ev.Data.(events.AssistantTextEndData); ok {
					if strings.TrimSpace(d.Text) != "" {
						fmt.Fprintf(w, "[assistant] %s\n", d.Text) //nolint:errcheck
						lastAssistantText = d.Text
					}
					if d.Reasoning != "" {
						fmt.Fprintf(w, "[thinking] (%d chars)\n", len(d.Reasoning)) //nolint:errcheck
					}
					usage := d.Usage
					line := fmt.Sprintf("[usage] in=%d out=%d total=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
					if usage.CacheReadTokens != nil {
						line += fmt.Sprintf(" cache_read=%d", *usage.CacheReadTokens)
					}
					if usage.CacheWriteTokens != nil {
						line += fmt.Sprintf(" cache_write=%d", *usage.CacheWriteTokens)
					}
					fmt.Fprintln(w, line) //nolint:errcheck
				}
			case events.EventToolCallStart:
				if d, ok := ev.Data.(events.ToolCallStartData); ok {
					args := d.ArgumentsJSON
					if len(args) > 100 {
						args = args[:97] + "..."
					}
					fmt.Fprintf(w, "[tool] %s %s\n", d.ToolName, args) //nolint:errcheck
				}
			case events.EventToolCallEnd:
				if d, ok := ev.Data.(events.ToolCallEndData); ok {
					if d.Error != "" {
						fmt.Fprintf(w, "[tool] %s: error\n", d.ToolName) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[tool] %s: done\n", d.ToolName) //nolint:errcheck
					}
				}
			case events.EventToolCallRepaired:
				if d, ok := ev.Data.(events.ToolCallRepairedData); ok {
					fmt.Fprintf(w, "[tool] ↻ repaired %s: %s\n", d.ToolName, strings.Join(d.Changes, ", ")) //nolint:errcheck
				}
			case events.EventCommunicate:
				if d, ok := ev.Data.(events.CommunicateData); ok {
					if apptranscript.EchoesAssistantText(lastAssistantText, d.Message) {
						continue
					}
					// A blank communicate says nothing, so it must not make the
					// printer forget the answer an echo would follow.
					if strings.TrimSpace(d.Message) != "" {
						lastAssistantText = d.Message
					}
					if d.EndTurn {
						fmt.Fprintf(w, "[communicate:end_turn] %s\n", d.Message) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[communicate] %s\n", d.Message) //nolint:errcheck
					}
				}
			case events.EventPluginLoaded:
				if d, ok := ev.Data.(events.PluginLoadedData); ok {
					fmt.Fprintf(w, "[plugin] loaded %s (%d skills, %d agents, %d mcp)\n", //nolint:errcheck
						d.Name, d.SkillCount, d.AgentCount, d.MCPCount)
				}
			case events.EventHookStart:
				if d, ok := ev.Data.(events.HookStartData); ok {
					fmt.Fprintf(w, "[hook] %s %s (%s)\n", d.Event, d.Matcher, d.HookType) //nolint:errcheck
				}
			case events.EventHookEnd:
				if d, ok := ev.Data.(events.HookEndData); ok {
					fmt.Fprintf(w, "[hook] %s %s done (%dms)\n", d.Event, d.Matcher, d.DurationMS) //nolint:errcheck
				}
			case events.EventSkillActivated:
				if d, ok := ev.Data.(events.SkillActivatedData); ok {
					fmt.Fprintf(w, "[skill] activated %s\n", d.Name) //nolint:errcheck
				}
			case events.EventWarning:
				if d, ok := ev.Data.(events.WarningData); ok {
					if d.Code != "" {
						fmt.Fprintf(w, "[warning:%s] %s\n", d.Code, d.Message) //nolint:errcheck
					} else {
						fmt.Fprintf(w, "[warning] %s\n", d.Message) //nolint:errcheck
					}
				}
			case events.EventError:
				if d, ok := ev.Data.(events.ErrorData); ok {
					fmt.Fprintf(w, "[error] %s\n", d.Error) //nolint:errcheck
				}
			}
		}
	}()
	return done
}

// resolveSessionMeta loads the session meta for the given resume configuration.
func resolveSessionMeta(cfg runConfig, stateDir string) (schema.SessionMeta, error) {
	id := cfg.resume
	if id == "" {
		id = cfg.resumeWith
	}
	return cmdutil.ResolveSessionMeta(stateDir, id, cfg.resumeLast)
}

// listSessions prints all saved sessions and returns.
func listSessions(cfg runConfig, stateDir string) error {
	list, err := schema.ListSessionMetas(stateDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(cfg.stdout, "No saved sessions.") //nolint:errcheck
		return nil
	}
	for _, m := range list {
		if identifier.ValidateSessionID(m.ID) != nil {
			continue
		}
		branch := m.EnvInfo.GitBranch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(cfg.stdout, "%s  %-16s  %-20s  %-20s  turns=%d\n", //nolint:errcheck
			m.ID, m.Model, branch, m.UpdatedAt.Format("2006-01-02 15:04:05"), m.TurnCount)
	}
	return nil
}
