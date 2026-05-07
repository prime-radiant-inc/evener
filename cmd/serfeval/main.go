// serfeval runs a serf task with a given context strategy and outputs
// structured evaluation metrics as JSON.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
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

func main() {
	provider := flag.String("provider", "", "LLM provider")
	model := flag.String("model", "", "LLM model identifier")
	strategy := flag.String("strategy", "compact", "context strategy: compact|recall|session-log|ooda|obs-mask|checkpoint-pred|memory-crystals|recursive-distill")
	task := flag.String("task", "", "task description")
	workDir := flag.String("dir", ".", "working directory")
	output := flag.String("output", "", "output JSON file (default: stdout)")
	probes := flag.String("probes", "", "path to JSON file with retention probe questions")
	thresholdScale := flag.Float64("threshold-scale", 0, "multiply compaction thresholds (e.g., 0.1 = trigger at 10% of normal)")
	maxTurns := flag.Int("max-turns", 0, "maximum agent turns before stopping (0 = unlimited)")
	testPatch := flag.String("test-patch", "", "path to SWE-bench test patch file for F2P evaluation")
	testCmd := flag.String("test-cmd", "", "command to run F2P tests (e.g., 'python -m pytest tests/test_foo.py')")
	f2pTests := flag.String("f2p-tests", "", "comma-separated fail-to-pass test names")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning effort: none|low|medium|high|xhigh")
	stateDir := flag.String("state-dir", "", "override runtime state directory (default: temp dir)")
	systemPrompt := flag.String("system-prompt", "", "path to a custom system prompt file")
	var systemPromptAppend cmdutil.StringSliceFlag
	flag.Var(&systemPromptAppend, "system-prompt-append", "path to append to system prompt (repeatable)")
	maxSubagentDepth := flag.Int("max-subagent-depth", -1, "max subagent nesting depth")
	shareTaskStore := flag.Bool("share-task-store", false, "share task list between parent and child sessions")
	resultToolName := flag.String("result-tool-name", "", "override the result tool name")
	exportATIF := flag.String("export-atif", "", "export ATIF trajectory to this path")
	verbose := flag.Bool("verbose", false, "emit NDJSON events to stderr")
	noProjectPrompts := flag.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading")
	agentName := flag.String("agent", "", "agent persona name (default: default)")
	var skillsDirs cmdutil.StringSliceFlag
	flag.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers cmdutil.StringSliceFlag
	flag.Var(&mcpServers, "mcp", "MCP server (repeatable)")
	var mcpConfigs cmdutil.StringSliceFlag
	flag.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
	var pluginDirs cmdutil.StringSliceFlag
	flag.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")
	cpuProfile := flag.String("cpu-profile", "", "write CPU profile to file")
	traceFile := flag.String("trace", "", "write execution trace to file")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serfeval --provider <p> --model <m> --task <task> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Run a serf task and output evaluation metrics as JSON.\n\n")
		fmt.Fprintf(os.Stderr, "Required:\n")
		fmt.Fprintf(os.Stderr, "  --provider <name>    LLM provider: openai, anthropic, google, minimax, openrouter, kimi, glm, ollama\n")
		fmt.Fprintf(os.Stderr, "  --model <name>       LLM model\n")
		fmt.Fprintf(os.Stderr, "  --task <text>        Task description\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  --strategy <name>    Context strategy (default: compact)\n")
		fmt.Fprintf(os.Stderr, "  --dir <path>         Working directory (default: .)\n")
		fmt.Fprintf(os.Stderr, "  --output <path>      Write JSON to file instead of stdout\n")
		fmt.Fprintf(os.Stderr, "  --probes <path>      JSON file with retention probe questions\n")
		fmt.Fprintf(os.Stderr, "  --threshold-scale <f> Multiply compaction thresholds (e.g., 0.1)\n")
		fmt.Fprintf(os.Stderr, "  --max-turns <n>      Maximum agent turns (0 = unlimited)\n")
		fmt.Fprintf(os.Stderr, "  --test-patch <path>  SWE-bench test patch for F2P evaluation\n")
		fmt.Fprintf(os.Stderr, "  --test-cmd <cmd>     Command to run F2P tests\n")
		fmt.Fprintf(os.Stderr, "  --f2p-tests <names>  Comma-separated fail-to-pass test names\n")
		fmt.Fprintf(os.Stderr, "  --reasoning-effort <level> Reasoning effort: none|low|medium|high|xhigh\n")
	}
	flag.Parse()

	if *provider == "" || *model == "" || *task == "" {
		flag.Usage()
		os.Exit(1)
	}

	cfg := evalConfig{
		provider:           *provider,
		model:              *model,
		strategy:           *strategy,
		task:               *task,
		workDir:            *workDir,
		output:             *output,
		probesFile:         *probes,
		thresholdScale:     *thresholdScale,
		maxTurns:           *maxTurns,
		testPatch:          *testPatch,
		testCmd:            *testCmd,
		f2pTests:           *f2pTests,
		reasoningEffort:    *reasoningEffort,
		stateDir:           *stateDir,
		systemPrompt:       *systemPrompt,
		systemPromptAppend: []string(systemPromptAppend),
		maxSubagentDepth:   *maxSubagentDepth,
		shareTaskStore:     *shareTaskStore,
		resultToolName:     *resultToolName,
		exportATIF:         *exportATIF,
		verbose:            *verbose,
		noProjectPrompts:   *noProjectPrompts,
		agentName:          *agentName,
		skillsDirs:         []string(skillsDirs),
		mcpServers:         []string(mcpServers),
		mcpConfigs:         []string(mcpConfigs),
		pluginDirs:         []string(pluginDirs),
	}

	if *cpuProfile != "" {
		stop, err := cmdutil.StartCPUProfile(*cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serfeval: %v\n", err)
			os.Exit(1)
		}
		defer stop()
	}
	if *traceFile != "" {
		stop, err := cmdutil.StartTrace(*traceFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serfeval: %v\n", err)
			os.Exit(1)
		}
		defer stop()
	}

	if err := runEval(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "serfeval: %v\n", err)
		os.Exit(1)
	}
}

type evalConfig struct {
	provider        string
	model           string
	strategy        string
	task            string
	workDir         string
	output          string
	probesFile      string
	thresholdScale  float64
	maxTurns        int
	testPatch       string
	testCmd         string
	f2pTests        string
	reasoningEffort string

	stateDir           string
	systemPrompt       string
	systemPromptAppend []string
	maxSubagentDepth   int
	shareTaskStore     bool
	resultToolName     string
	exportATIF         string
	verbose            bool
	noProjectPrompts   bool
	agentName          string
	skillsDirs         []string
	mcpServers         []string
	mcpConfigs         []string
	pluginDirs         []string
}

func runEval(cfg evalConfig) error {
	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	profile, err := cmdutil.SelectProfile(cfg.provider, cfg.model, "")
	if err != nil {
		return err
	}

	env := agent.NewLocalExecutionEnvironment(cfg.workDir)

	stateDir := cfg.stateDir
	if stateDir == "" {
		var tmpErr error
		stateDir, tmpErr = os.MkdirTemp("", "serfeval-state-")
		if tmpErr != nil {
			return fmt.Errorf("create state dir: %w", tmpErr)
		}
		defer os.RemoveAll(stateDir)
	}

	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
	}

	sessCfg := agent.SessionConfig{
		ContextStrategy:          cfg.strategy,
		CompactionThresholdScale: cfg.thresholdScale,
		StateDir:                 stateDir,
		MaxToolRoundsPerInput:    cfg.maxTurns,
		ShareTasksWithChildren:   cfg.shareTaskStore,
		ResultToolName:           cfg.resultToolName,
		SystemPromptFile:         cfg.systemPrompt,
		SystemPromptAppend:       cfg.systemPromptAppend,
		NoProjectPrompts:         cfg.noProjectPrompts,
		AgentName:                cfg.agentName,
		SkillsDirs:               cfg.skillsDirs,
		MCPConfigFiles:           cfg.mcpConfigs,
		MCPInline:                cfg.mcpServers,
		PluginDirs:               cfg.pluginDirs,
		ExportATIFPath:           cfg.exportATIF,
		NonInteractive:           true,
	}
	if cfg.maxSubagentDepth >= 0 {
		sessCfg.MaxSubagentDepth = cfg.maxSubagentDepth
	}
	if effort.Set {
		sessCfg.ReasoningEffort = effort.Value
	}

	sess, err := agent.NewSession(client, profile, env, sessCfg)
	if err != nil {
		return fmt.Errorf("session creation: %w", err)
	}
	defer sess.Close()

	collector := agent.NewEvalCollector(cfg.strategy, cfg.model, cfg.task)

	// Drain events in background.
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			collector.ProcessEvent(ev)
			if cfg.verbose {
				data, _ := json.Marshal(ev)
				fmt.Fprintln(os.Stderr, string(data))
			}
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	start := time.Now()
	result, taskErr := sess.ProcessInput(ctx, cfg.task)
	elapsed := time.Since(start)

	// Grab session history before closing for retention probes.
	snap := sess.Snapshot()

	sess.Close()
	<-eventsDone

	metrics := collector.Metrics()
	metrics.DurationSeconds = elapsed.Seconds()
	metrics.Completed = taskErr == nil
	metrics.Result = result

	// Capture git diff of agent's changes (before F2P test patch or cleanup).
	// Use git add -N to track new files, then diff shows everything.
	addNCmd := exec.Command("git", "add", "-N", ".")
	addNCmd.Dir = cfg.workDir
	addNCmd.Run() //nolint:errcheck // best effort
	diffCmd := exec.Command("git", "diff")
	diffCmd.Dir = cfg.workDir
	if diffOut, diffErr := diffCmd.Output(); diffErr == nil {
		metrics.Diff = string(diffOut)
	}
	// Reset the intent-to-add so it doesn't interfere with F2P tests.
	resetCmd := exec.Command("git", "reset", "-q")
	resetCmd.Dir = cfg.workDir
	resetCmd.Run() //nolint:errcheck // best effort

	// Run F2P test evaluation if test patch was provided.
	if cfg.testPatch != "" && cfg.testCmd != "" {
		f2p := runF2PTests(cfg.workDir, cfg.testPatch, cfg.testCmd, cfg.f2pTests)
		metrics.F2PResults = &f2p
	}

	// Run retention probes if a probes file was provided.
	if cfg.probesFile != "" {
		probeQuestions, loadErr := loadProbeQuestions(cfg.probesFile)
		if loadErr != nil {
			return fmt.Errorf("load probes: %w", loadErr)
		}
		// Use a fresh context for probes -- the task context may have been cancelled.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer probeCancel()
		retScore, breakdown, probeErr := agent.RunRetentionProbes(probeCtx, client, profile, probeQuestions, snap.History)
		if probeErr != nil {
			return fmt.Errorf("retention probes: %w", probeErr)
		}
		metrics.RetentionScore = retScore
		metrics.RetentionBreakdown = breakdown
	}

	data, marshalErr := json.MarshalIndent(metrics, "", "  ")
	if marshalErr != nil {
		return fmt.Errorf("marshal metrics: %w", marshalErr)
	}

	if cfg.output != "" {
		if err := os.WriteFile(cfg.output, append(data, '\n'), 0o644); err != nil {
			return err
		}
		// Save session snapshot alongside the output for post-hoc analysis.
		snapPath := strings.TrimSuffix(cfg.output, ".json") + ".session.json"
		snapData, snapErr := json.MarshalIndent(snap, "", "  ")
		if snapErr == nil {
			os.WriteFile(snapPath, append(snapData, '\n'), 0o644) //nolint:errcheck
		}
		return nil
	}
	fmt.Println(string(data))
	return nil
}

// loadProbeQuestions reads a JSON file with the new probe format:
//
//	{"questions": [{"question": "...", "expected": "...", "difficulty": "...", "type": "..."}]}
func loadProbeQuestions(path string) ([]agent.ProbeQuestion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probeFile struct {
		Questions []agent.ProbeQuestion `json:"questions"`
	}
	if err := json.Unmarshal(data, &probeFile); err != nil {
		return nil, fmt.Errorf("parse probes JSON: %w", err)
	}
	if len(probeFile.Questions) == 0 {
		return nil, fmt.Errorf("probes file contains no questions")
	}
	return probeFile.Questions, nil
}

// runF2PTests applies the test patch, runs the test command, and checks which
// F2P tests passed. Always cleans up the patch afterward.
func runF2PTests(workDir, testPatchPath, testCmd, f2pTestNames string) agent.F2PResults {
	result := agent.F2PResults{}

	// Apply the test patch.
	patchData, err := os.ReadFile(testPatchPath)
	if err != nil {
		result.TestErrors = fmt.Sprintf("read test patch: %v", err)
		return result
	}

	applyCmd := exec.Command("git", "apply", "--allow-empty", "-")
	applyCmd.Dir = workDir
	applyCmd.Stdin = bytes.NewReader(patchData)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		result.TestErrors = fmt.Sprintf("git apply test patch: %v\n%s", err, out)
		return result
	}
	result.PatchApplied = true

	// Always clean up the patch when done.
	defer func() {
		reverseCmd := exec.Command("git", "apply", "--reverse", "--allow-empty", "-")
		reverseCmd.Dir = workDir
		reverseCmd.Stdin = bytes.NewReader(patchData)
		reverseCmd.CombinedOutput() //nolint:errcheck // best effort
	}()

	// Run the test command.
	parts := strings.Fields(testCmd)
	if len(parts) == 0 {
		result.TestErrors = "empty test command"
		return result
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = workDir
	testOutput, testErr := cmd.CombinedOutput()

	// Parse F2P test results from the test command output.
	f2pNames := parseF2PTestNames(f2pTestNames)
	outputStr := string(testOutput)

	for _, name := range f2pNames {
		if testErr == nil || strings.Contains(outputStr, name+" PASSED") || strings.Contains(outputStr, name+" passed") || isTestPassed(outputStr, name) {
			result.TestsPassed = append(result.TestsPassed, name)
		} else {
			result.TestsFailed = append(result.TestsFailed, name)
		}
	}

	// If the test command succeeded entirely and all F2P tests are in the passed list,
	// mark as resolved.
	if testErr == nil {
		result.Resolved = true
	} else if len(result.TestsFailed) == 0 && len(result.TestsPassed) == len(f2pNames) {
		result.Resolved = true
	}

	if testErr != nil && len(result.TestsFailed) > 0 {
		result.TestErrors = fmt.Sprintf("test command failed: %v", testErr)
	}

	return result
}

// parseF2PTestNames splits a comma-separated test name string.
func parseF2PTestNames(names string) []string {
	if names == "" {
		return nil
	}
	parts := strings.Split(names, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// isTestPassed checks common test output patterns to see if a specific test passed.
func isTestPassed(output, testName string) bool {
	// pytest output: "PASSED" after the test name
	if strings.Contains(output, testName+" PASSED") {
		return true
	}
	// pytest verbose: test name followed by PASSED
	if strings.Contains(output, testName+"::") {
		// If the test name appears and there's no FAILED/ERROR after it
		idx := strings.Index(output, testName)
		remainder := output[idx:]
		nextNewline := strings.IndexByte(remainder, '\n')
		if nextNewline > 0 {
			line := remainder[:nextNewline]
			if strings.Contains(line, "PASSED") {
				return true
			}
		}
	}
	return false
}
