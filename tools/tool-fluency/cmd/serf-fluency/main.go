package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/doctor"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
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

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "serf-fluency:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "catalog":
		return runCatalog(args[1:])
	case "run":
		return runSuite(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `serf-fluency measures model-facing Serf tool fluency.

USAGE
  serf-fluency catalog [--model provider/model] [--json]
  serf-fluency run [--model provider/model] [--probe id] [--build]

`)
}

type catalogTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

func runCatalog(args []string) error {
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	model := fs.String("model", defaultModel(), "provider/model")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tools, err := catalogTools(*model)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tools)
	}
	for _, tool := range tools {
		fmt.Println(tool.Name)
	}
	return nil
}

func catalogTools(modelRef string) ([]catalogTool, error) {
	providerName, modelName, err := splitModelRef(modelRef)
	if err != nil {
		return nil, err
	}
	profile, err := cmdutil.ResolveProfileForProvider(providerName, modelName)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "serf-fluency-catalog-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	client := llm.NewClient()
	sess, err := agent.NewSession(client, profile, execenv.NewLocalExecutionEnvironment(tmp), agent.SessionConfig{
		StateDir:         filepath.Join(tmp, "state"),
		NoProjectPrompts: true,
		NonInteractive:   true,
	})
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	defs := sess.ToolDefinitions()
	out := make([]catalogTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, catalogTool{Name: def.Name, Description: def.Description, Strict: def.Strict})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func splitModelRef(ref string) (string, string, error) {
	provider, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("model %q must be provider/model", ref)
	}
	return provider, model, nil
}

type probeFile struct {
	Schema  int               `yaml:"schema"`
	ID      string            `yaml:"id"`
	Tool    string            `yaml:"tool"`
	Prompt  string            `yaml:"prompt"`
	Fixture fixtureSpec       `yaml:"fixture"`
	Expect  expectSpec        `yaml:"expect"`
	Metrics map[string]any    `yaml:"metrics"`
	Skip    map[string]string `yaml:"skip,omitempty"`
}

type fixtureSpec struct {
	Files map[string]string `yaml:"files"`
}

type expectSpec struct {
	Calls          []expectedCall   `yaml:"calls"`
	ForbiddenCalls []string         `yaml:"forbidden_calls"`
	Artifacts      []artifactExpect `yaml:"artifacts"`
	FinalContains  []string         `yaml:"final_contains"`
}

type expectedCall struct {
	Tool string `yaml:"tool" json:"tool"`
	Min  int    `yaml:"min,omitempty" json:"min,omitempty"`
}

type artifactExpect struct {
	Path     string `yaml:"path" json:"path"`
	Exists   *bool  `yaml:"exists,omitempty" json:"exists,omitempty"`
	Contains string `yaml:"contains,omitempty" json:"contains,omitempty"`
}

type runConfig struct {
	model             string
	fastCheapModel    string
	harness           string
	probesDir         string
	probeFilter       string
	outDir            string
	serfBin           string
	build             bool
	repetitions       int
	timeout           time.Duration
	postTurnWait      time.Duration
	reasoningEffort   string
	clearOpenAIAPIKey bool
}

func runSuite(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfg := runConfig{}
	fs.StringVar(&cfg.model, "model", defaultModel(), "provider/model")
	fs.StringVar(&cfg.fastCheapModel, "fast-cheap-model", "", "provider/model or bare model for Serf auxiliary side calls")
	fs.StringVar(&cfg.harness, "harness", "cli", "execution harness: cli or live")
	fs.StringVar(&cfg.probesDir, "probes-dir", "tools/tool-fluency/probes", "probe manifest directory")
	fs.StringVar(&cfg.probeFilter, "probe", "all", "probe id or all")
	fs.StringVar(&cfg.outDir, "out", "", "result directory")
	fs.StringVar(&cfg.serfBin, "serf-bin", "", "serf binary to run")
	fs.BoolVar(&cfg.build, "build", false, "build a fresh serf binary before running")
	fs.IntVar(&cfg.repetitions, "repetitions", 1, "repetitions per probe")
	fs.DurationVar(&cfg.timeout, "timeout", 8*time.Minute, "timeout per probe repetition")
	fs.DurationVar(&cfg.postTurnWait, "post-turn-wait", 45*time.Second, "live harness post-root-turn wait window")
	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "high", "reasoning effort")
	fs.BoolVar(&cfg.clearOpenAIAPIKey, "clear-openai-api-key", false, "clear OPENAI_API_KEY for OAuth-backed OpenAI runs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.repetitions < 1 {
		return errors.New("--repetitions must be >= 1")
	}
	if cfg.harness != "cli" && cfg.harness != "live" {
		return errors.New("--harness must be cli or live")
	}
	if cfg.outDir == "" {
		cfg.outDir = filepath.Join("tools", "tool-fluency", "results", time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return err
	}
	if cfg.harness == "cli" && (cfg.build || cfg.serfBin == "") {
		bin, err := buildSerf(cfg.outDir)
		if err != nil {
			return err
		}
		cfg.serfBin = bin
	}
	probes, err := loadProbes(cfg.probesDir, cfg.probeFilter)
	if err != nil {
		return err
	}
	if len(probes) == 0 {
		return errors.New("no probes selected")
	}
	catalog, err := catalogTools(cfg.model)
	if err != nil {
		return err
	}
	available := make(map[string]bool, len(catalog))
	for _, tool := range catalog {
		available[tool.Name] = true
	}
	fmt.Fprintf(os.Stderr, "[serf-fluency] model=%s probes=%d out=%s\n", cfg.model, len(probes), cfg.outDir)
	var results []probeResult
	for _, probe := range probes {
		for rep := 1; rep <= cfg.repetitions; rep++ {
			res := runProbe(cfg, probe, rep, available)
			results = append(results, res)
			if err := writeProbeResult(cfg.outDir, res); err != nil {
				return err
			}
			fmt.Printf("%-42s rep=%d status=%-20s calls=%s findings=%d\n",
				res.Probe, rep, res.Status, formatCounts(res.CanonicalToolCounts), len(res.Findings))
		}
	}
	return writeSummary(cfg.outDir, results)
}

func defaultModel() string {
	if v := strings.TrimSpace(os.Getenv("SERF_FLUENCY_MODEL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("SERF_MODEL")); v != "" {
		return v
	}
	return "openai/gpt-5.4-mini"
}

func buildSerf(outDir string) (string, error) {
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	bin := filepath.Join(binDir, "serf")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/serf")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build serf: %w", err)
	}
	return bin, nil
}

func loadProbes(dir, filter string) ([]probeFile, error) {
	var probes []probeFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var probe probeFile
		if err := yaml.Unmarshal(data, &probe); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if probe.ID == "" {
			return fmt.Errorf("%s: missing id", path)
		}
		if filter == "all" || filter == probe.ID {
			probes = append(probes, probe)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].ID < probes[j].ID })
	return probes, nil
}

type probeResult struct {
	Schema              int            `json:"schema"`
	Probe               string         `json:"probe"`
	Tool                string         `json:"tool,omitempty"`
	Model               string         `json:"model"`
	FastCheapModel      string         `json:"fast_cheap_model,omitempty"`
	Repetition          int            `json:"repetition"`
	Status              string         `json:"status"`
	SessionID           string         `json:"session_id,omitempty"`
	WorkDir             string         `json:"work_dir"`
	StateDir            string         `json:"state_dir"`
	StdoutPath          string         `json:"stdout_path"`
	StderrPath          string         `json:"stderr_path"`
	FinalOutput         string         `json:"final_output,omitempty"`
	CommunicateMessages []string       `json:"communicate_messages,omitempty"`
	ModelToolCounts     map[string]int `json:"model_tool_counts,omitempty"`
	CanonicalToolCounts map[string]int `json:"canonical_tool_counts,omitempty"`
	ToolErrors          map[string]int `json:"tool_errors,omitempty"`
	Findings            []finding      `json:"findings"`
	DurationMS          int64          `json:"duration_ms"`
	Error               string         `json:"error,omitempty"`
}

type finding struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

func runProbe(cfg runConfig, probe probeFile, rep int, available map[string]bool) probeResult {
	start := time.Now()
	slug := safeName(probe.ID)
	base := filepath.Join(cfg.outDir, slug, fmt.Sprintf("rep-%02d", rep))
	workDir := filepath.Join(base, "work")
	stateDir := filepath.Join(base, "state")
	stdoutPath := filepath.Join(base, "stdout.txt")
	stderrPath := filepath.Join(base, "stderr.ndjson")
	res := probeResult{
		Schema:              1,
		Probe:               probe.ID,
		Tool:                probe.Tool,
		Model:               cfg.model,
		FastCheapModel:      strings.TrimSpace(cfg.fastCheapModel),
		Repetition:          rep,
		Status:              "failed",
		WorkDir:             workDir,
		StateDir:            stateDir,
		StdoutPath:          stdoutPath,
		StderrPath:          stderrPath,
		ModelToolCounts:     map[string]int{},
		CanonicalToolCounts: map[string]int{},
		ToolErrors:          map[string]int{},
	}
	if skip := unavailableFinding(probe, available); skip != nil {
		res.Status = "skipped_unavailable"
		res.Findings = append(res.Findings, *skip)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	if err := materializeFixture(workDir, probe.Fixture); err != nil {
		res.Error = err.Error()
		res.Findings = append(res.Findings, finding{Category: "infra", Title: "fixture setup failed", Detail: err.Error()})
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	var err error
	if cfg.harness == "live" {
		err = runLiveProbe(ctx, cfg, probe, &res, &stdout, &stderr)
	} else {
		err = runCLIProbe(ctx, cfg, probe, res, &stdout, &stderr)
	}
	_ = os.MkdirAll(base, 0o755)
	_ = os.WriteFile(stdoutPath, stdout.Bytes(), 0o644)
	_ = os.WriteFile(stderrPath, stderr.Bytes(), 0o644)
	res.FinalOutput = strings.TrimSpace(stdout.String())
	res.CanonicalToolCounts, res.ToolErrors, res.CommunicateMessages = parseEvents(stderr.Bytes())
	if id, err := rootSessionID(stateDir); err == nil {
		res.SessionID = id
		if tr, err := doctor.Transcript(stateDir, id, doctor.TranscriptOpts{}); err == nil {
			res.ModelToolCounts = transcriptToolCounts(tr)
		}
	}
	if err != nil {
		res.Error = err.Error()
		category := "runtime"
		if ctx.Err() != nil || looksInfraError(stderr.String()) {
			category = "infra"
			res.Status = "blocked_infra"
		}
		res.Findings = append(res.Findings, finding{Category: category, Title: "probe command failed", Detail: err.Error()})
	}
	res.Findings = append(res.Findings, evaluateExpectations(workDir, probe, res)...)
	if len(res.Findings) == 0 {
		res.Status = "passed"
	} else if res.Status == "failed" {
		res.Status = "failed"
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

func runCLIProbe(ctx context.Context, cfg runConfig, probe probeFile, res probeResult, stdout, stderr *bytes.Buffer) error {
	args := []string{
		"--model", cfg.model,
	}
	if strings.TrimSpace(cfg.fastCheapModel) != "" {
		args = append(args, "--fast-cheap-model", cfg.fastCheapModel)
	}
	args = append(args,
		"--dir", res.WorkDir,
		"--state-dir", res.StateDir,
		"--reasoning-effort", cfg.reasoningEffort,
		"--context-strategy", "compact",
		"--max-rounds", "80",
		"--no-project-prompts",
		"--verbose",
		probe.Prompt,
	)
	cmd := exec.CommandContext(ctx, cfg.serfBin, args...)
	cmd.Env = os.Environ()
	if cfg.clearOpenAIAPIKey {
		cmd.Env = append(cmd.Env, "OPENAI_API_KEY=")
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type liveKick struct {
	kind  agent.EntryKind
	input string
}

func runLiveProbe(ctx context.Context, cfg runConfig, probe probeFile, res *probeResult, stdout, stderr *bytes.Buffer) error {
	restoreEnv := maybeClearOpenAIAPIKey(cfg.clearOpenAIAPIKey)
	defer restoreEnv()

	if err := cmdutil.EnsureUserConfigDirs(); err != nil {
		return err
	}
	modelRef, err := cmdutil.ParseModelRef(cfg.model)
	if err != nil {
		return err
	}
	client, provCfg, hasProvConfig, err := cmdutil.LoadClient(llm.WithStateDir(res.StateDir))
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}
	closeAPILog, err := cmdutil.AttachAPILogger(client, res.StateDir, stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck

	profile, err := runnerInitialProfile(provCfg, modelRef)
	if err != nil {
		return err
	}
	profile, err = runnerApplyFastCheapModel(profile, cfg.fastCheapModel, client)
	if err != nil {
		return err
	}
	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
	}

	sessCfg := agent.SessionConfig{
		MaxToolRoundsPerInput: cmdutil.MaxRoundsToConfig(80),
		StateDir:              res.StateDir,
		NoProjectPrompts:      true,
		NonInteractive:        true,
		ContextStrategy:       "compact",
		ResolveProfile:        cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
	}
	if effort.Set {
		sessCfg.ReasoningEffort = effort.Value
	}
	sess, err := agent.NewSession(client, profile, execenv.NewLocalExecutionEnvironment(res.WorkDir), sessCfg)
	if err != nil {
		return err
	}
	res.SessionID = sess.ID()

	var stderrMu sync.Mutex
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			line, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			stderrMu.Lock()
			stderr.Write(line)
			stderr.WriteByte('\n')
			stderrMu.Unlock()
		}
	}()

	kicks := make(chan liveKick, 16)
	submitKick := func(k liveKick) {
		select {
		case kicks <- k:
		default:
		}
	}
	sess.SetKickFunc(func(prompt string) {
		submitKick(liveKick{kind: agent.EntryContinuation, input: prompt})
	})
	sess.SetNotifyFunc(func() {
		submitKick(liveKick{kind: agent.EntryNotification})
	})

	closeSession := func() {
		sess.Close()
		<-eventsDone
	}
	defer closeSession()

	out, err := sess.ProcessInput(ctx, probe.Prompt, nil)
	if strings.TrimSpace(out) != "" {
		stdout.WriteString(out)
		stdout.WriteByte('\n')
	}
	if err != nil {
		return err
	}
	if cfg.postTurnWait <= 0 {
		return nil
	}

	timer := time.NewTimer(cfg.postTurnWait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case kick := <-kicks:
			out, err := sess.ProcessInputKind(ctx, kick.input, nil, kick.kind)
			if strings.TrimSpace(out) != "" {
				stdout.WriteString(out)
				stdout.WriteByte('\n')
			}
			if err != nil {
				return err
			}
		}
	}
}

func maybeClearOpenAIAPIKey(clear bool) func() {
	if !clear {
		return func() {}
	}
	old, ok := os.LookupEnv("OPENAI_API_KEY")
	_ = os.Unsetenv("OPENAI_API_KEY")
	return func() {
		if ok {
			_ = os.Setenv("OPENAI_API_KEY", old)
		} else {
			_ = os.Unsetenv("OPENAI_API_KEY")
		}
	}
}

func runnerInitialProfile(cfg providercfg.Config, modelRef cmdutil.ModelRef) (*provider.Profile, error) {
	raw, err := cmdutil.ResolveProfileWithLiveWindow(cfg, modelRef.Qualified())
	if err != nil {
		return nil, err
	}
	return provider.WithAllowedDecisions(raw, cmdutil.ParseAllowedDecisions(os.Getenv("SERF_ALLOWED_DECISIONS"))), nil
}

func runnerApplyFastCheapModel(profile *provider.Profile, raw string, client *llm.Client) (*provider.Profile, error) {
	if profile == nil || strings.TrimSpace(raw) == "" {
		return profile, nil
	}
	raw = strings.TrimSpace(raw)
	if cheapProvider, model, ok := strings.Cut(raw, "/"); ok && cheapProvider != "" && model != "" && cheapProvider != profile.ID() {
		if !runnerClientHasProvider(client, cheapProvider) {
			return nil, fmt.Errorf("--fast-cheap-model provider %q is not configured or has no credential (active provider %q); available providers: %s",
				cheapProvider, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return provider.WithCheapModel(profile, raw), nil
}

func runnerClientHasProvider(client *llm.Client, name string) bool {
	if client == nil {
		return false
	}
	for _, p := range client.ProviderNames() {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

func unavailableFinding(probe probeFile, available map[string]bool) *finding {
	name := strings.TrimSpace(probe.Skip["if_unavailable"])
	if name == "" {
		return nil
	}
	if !available[name] {
		return &finding{
			Category: "availability",
			Title:    "tool is not advertised in this context",
			Detail:   name,
		}
	}
	return nil
}

func materializeFixture(workDir string, fixture fixtureSpec) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	for rel, content := range fixture.Files {
		path := filepath.Join(workDir, filepath.Clean(rel))
		within, err := filepath.Rel(workDir, path)
		if err != nil || strings.HasPrefix(within, "..") || filepath.IsAbs(within) {
			return fmt.Errorf("fixture path escapes workdir: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func parseEvents(data []byte) (map[string]int, map[string]int, []string) {
	counts := map[string]int{}
	errorsByTool := map[string]int{}
	var communicateMessages []string
	for _, raw := range bytes.Split(data, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var ev struct {
			Kind string `json:"kind"`
			Data struct {
				ToolName string `json:"tool_name"`
				Error    string `json:"error"`
				Message  string `json:"message"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		switch events.EventKind(ev.Kind) {
		case events.EventToolCallStart:
			if ev.Data.ToolName != "" {
				counts[ev.Data.ToolName]++
			}
		case events.EventToolCallEnd:
			if ev.Data.ToolName != "" && ev.Data.Error != "" {
				errorsByTool[ev.Data.ToolName]++
			}
		case events.EventCommunicate:
			if ev.Data.Message != "" {
				communicateMessages = append(communicateMessages, ev.Data.Message)
			}
		}
	}
	return counts, errorsByTool, communicateMessages
}

func transcriptToolCounts(tr doctor.TranscriptResult) map[string]int {
	counts := map[string]int{}
	for _, turn := range tr.Turns {
		for _, call := range turn.ToolCalls {
			counts[call.Name]++
		}
	}
	return counts
}

func rootSessionID(stateDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.meta.json"))
	if err != nil {
		return "", err
	}
	var selected schema.SessionMeta
	found := false
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var meta schema.SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.IsSubagent || meta.ParentSessionID != "" {
			continue
		}
		if !found || meta.CreatedAt.Before(selected.CreatedAt) {
			selected = meta
			found = true
		}
	}
	if !found {
		return "", errors.New("root session meta not found")
	}
	return selected.ID, nil
}

func evaluateExpectations(workDir string, probe probeFile, res probeResult) []finding {
	var out []finding
	for _, call := range probe.Expect.Calls {
		minCalls := call.Min
		if minCalls == 0 {
			minCalls = 1
		}
		got := max(res.CanonicalToolCounts[call.Tool], res.ModelToolCounts[call.Tool])
		if got < minCalls {
			out = append(out, finding{
				Category: "selection",
				Title:    "expected tool was not called enough",
				Detail:   fmt.Sprintf("%s got=%d want>=%d", call.Tool, got, minCalls),
			})
		}
	}
	for _, name := range probe.Expect.ForbiddenCalls {
		got := max(res.CanonicalToolCounts[name], res.ModelToolCounts[name])
		if got > 0 {
			out = append(out, finding{
				Category: "churn",
				Title:    "forbidden tool was called",
				Detail:   fmt.Sprintf("%s calls=%d", name, got),
			})
		}
	}
	for _, artifact := range probe.Expect.Artifacts {
		path := filepath.Join(workDir, filepath.Clean(artifact.Path))
		info, err := os.Stat(path)
		exists := err == nil && !info.IsDir()
		if artifact.Exists != nil && *artifact.Exists != exists {
			out = append(out, finding{
				Category: "artifact",
				Title:    "artifact existence mismatch",
				Detail:   fmt.Sprintf("%s exists=%v want=%v", artifact.Path, exists, *artifact.Exists),
			})
			continue
		}
		if artifact.Contains != "" {
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), artifact.Contains) {
				out = append(out, finding{
					Category: "artifact",
					Title:    "artifact content mismatch",
					Detail:   artifact.Path,
				})
			}
		}
	}
	for _, want := range probe.Expect.FinalContains {
		if !resultContains(res, want) {
			out = append(out, finding{
				Category: "interpretation",
				Title:    "final output missing expected text",
				Detail:   want,
			})
		}
	}
	for toolName, n := range res.ToolErrors {
		if n > 0 {
			out = append(out, finding{
				Category: "arguments",
				Title:    "tool returned validation/runtime errors",
				Detail:   fmt.Sprintf("%s errors=%d", toolName, n),
			})
		}
	}
	return out
}

func resultContains(res probeResult, want string) bool {
	if strings.Contains(res.FinalOutput, want) {
		return true
	}
	for _, msg := range res.CommunicateMessages {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func looksInfraError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "429") ||
		strings.Contains(lower, "no provider") ||
		strings.Contains(lower, "unknown provider") ||
		strings.Contains(lower, "api key")
}

func writeProbeResult(outDir string, res probeResult) error {
	path := filepath.Join(outDir, safeName(res.Probe), fmt.Sprintf("rep-%02d", res.Repetition), "result.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	jsonl := filepath.Join(outDir, "results.jsonl")
	line, _ := json.Marshal(res)
	f, err := os.OpenFile(jsonl, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

func writeSummary(outDir string, results []probeResult) error {
	type row struct {
		Probe    string `json:"probe"`
		Tool     string `json:"tool,omitempty"`
		Model    string `json:"model"`
		Status   string `json:"status"`
		Findings int    `json:"findings"`
	}
	rows := make([]row, 0, len(results))
	for _, res := range results {
		rows = append(rows, row{Probe: res.Probe, Tool: res.Tool, Model: res.Model, Status: res.Status, Findings: len(res.Findings)})
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "summary.json"), append(data, '\n'), 0o644)
}

func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "-"
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s:%d", name, counts[name]))
	}
	return strings.Join(parts, ",")
}

func safeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
