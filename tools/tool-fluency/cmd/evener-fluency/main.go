package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/execsupport/procgroup"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

var exitProcess = os.Exit

// configureHermeticRunEnv hides the operator's personal skills from every
// run: it sets EVENER_NO_USER_SKILLS so neither an in-process live session
// nor a spawned evener child advertises the operator's home skills or user
// config skills. A round that measures skill use then depends only on the
// revision under test, not on who runs it (#3227). This is only half of
// hermeticity: the operator's installed, enabled plugins (hooks, agents,
// commands, and plugin-sourced skills) load in full whenever plugin
// resolution reaches its default root, which EVENER_NO_USER_SKILLS does not
// touch; cliProbeArgs closes that gap for the CLI harness by passing
// --enabled-plugins with an explicit empty selection.
//
// inheritOperatorEnv (--inherit-operator-env, default false) restores
// today's pre-#3227 behavior for debugging: it clears the variable instead
// of setting it, so a run sees the operator's real skills again.
//
// run sets the hermetic default before dispatching any subcommand, so every
// session evener-fluency builds (catalog's too) hides the operator's skills.
// runSuite and runMatrixCommand then apply their own flag once, after
// parsing and before running any probe, so concurrent matrix cells share one
// setting instead of racing on a per-cell set/restore.
func configureHermeticRunEnv(inheritOperatorEnv bool) {
	if inheritOperatorEnv {
		_ = envvars.EVENERNoUserSkills.Unsetenv()
		return
	}
	_ = envvars.EVENERNoUserSkills.Setenv("1")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "evener-fluency:", err)
		exitProcess(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}
	configureHermeticRunEnv(false)
	switch args[0] {
	case "catalog":
		return runCatalog(args[1:])
	case "run":
		return runSuite(args[1:])
	case "prose-stats":
		return runProseStats(args[1:])
	case "prose-count":
		return runProseCount(args[1:])
	case "review-pack":
		return runReviewPack(args[1:])
	case "rank-sets":
		return runRankSets(args[1:])
	case "rank-score":
		return runRankScore(args[1:])
	case "matrix":
		return runMatrixCommand(args[1:])
	case "respond":
		return runRespond(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `evener-fluency measures model-facing Evener tool fluency.

USAGE
  evener-fluency catalog [--model provider/model] [--json]
  evener-fluency run [--model provider/model] [--probe id] [--build]
  evener-fluency prose-stats --results LABEL=DIR [--results LABEL=DIR ...] [--channel to_user|all] [--json]
  evener-fluency prose-count FILE...
  evener-fluency review-pack --results LABEL=DIR [...] --mask-root DIR --packets DIR --key FILE [--seed N]
  evener-fluency rank-sets --review-pack-key FILE --packets DIR --out FILE --key FILE [--skip-task ID ...] [--seed N]
  evener-fluency rank-score --key FILE --reviews FILE [...] [--detail] [--json]
  evener-fluency matrix --version LABEL=BIN [...] --models M1,M2 --out DIR [--max-concurrent N] [run flags]
  evener-fluency matrix --version-manifest FILE --version-cache DIR --models M1,M2 --out DIR [run flags]
  evener-fluency respond --brief-file FILE --model provider/model [--log FILE]

`)
}

type catalogTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

var runnerNewSession = agent.NewSession

func runCatalog(args []string) error {
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	model := fs.String("model", defaultModel(), "provider/model")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reg, err := runnerLoadRegistry()
	if err != nil {
		return fmt.Errorf("provider registry: %w", err)
	}
	tools, err := catalogTools(reg, *model)
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

// runnerLoadRegistry loads the provider registry a run resolves its model on:
// the one evener itself loads, with the user's configured providers, so the
// runner reads a run the way evener ran it. TestMain replaces it with the
// embedded registry, so default tests never read a developer's configuration.
var runnerLoadRegistry = func() (*registry.Registry, error) {
	r, _, err := cmdutil.LoadRegistry()
	return r, err
}

// catalogTools lists the tools evener offers the model, resolved on reg. The
// catalog needs only the model's profile: no credentials and no network.
func catalogTools(reg *registry.Registry, modelRef string) ([]catalogTool, error) {
	providerName, modelName, err := splitModelRef(modelRef)
	if err != nil {
		return nil, err
	}
	profile, err := provider.Resolve(reg, providerName+"/"+modelName)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "evener-fluency-catalog-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = os.RemoveAll(tmp)
	}()
	client := llm.NewClient()
	sess, err := runnerNewSession(client, profile, execenv.NewLocalExecutionEnvironment(tmp), agent.SessionConfig{
		StateDir:       filepath.Join(tmp, "state"),
		NonInteractive: true,
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
	Metrics metricsSpec       `yaml:"metrics"`
	Skip    map[string]string `yaml:"skip,omitempty"`
	// Reference is a shell script that solves the task. Only the offline
	// task test runs it, to prove the checks can pass; the runner ignores it.
	Reference string `yaml:"reference,omitempty"`
	// Person, when set, makes the CLI harness point the probe's evener run at
	// --ask-responder: an evener-fluency respond invocation that plays this
	// person's part when the agent calls ask_user.
	Person *personSpec `yaml:"person,omitempty"`
}

// personSpec is a task manifest's optional "person:" block. When present,
// cliProbeArgs wires the probe's evener run to --ask-responder, pointed at
// this same binary's own "respond" subcommand: the model plays Person from
// Brief alone, in its own voice, and says "I don't know" for anything the
// brief does not cover (see respond in respond.go).
type personSpec struct {
	// Brief is who the person is and the facts only they know, passed to
	// `respond --brief-file` verbatim.
	Brief string `yaml:"brief"`
	// Model is the provider/model that plays the person; empty defaults to
	// the run's --fast-cheap-model.
	Model string `yaml:"model,omitempty"`
}

// metricsSpec is the validated form of a probe manifest's `metrics:` block
// (#187: the block was previously parsed into an untyped map that nothing
// read). Each field names a runner-computed metric the probe wants reported
// on its result; maxToolCalls is a manifest-local threshold.
type metricsSpec struct {
	MaxToolCalls                      int  `yaml:"max_tool_calls"`
	WantsInvestigativeCallCount       bool `yaml:"wants_investigative_call_count"`
	WantsRepeatedReadOrGrepCount      bool `yaml:"wants_repeated_read_or_grep_count"`
	WantsToolRoundOfFirstTestRun      bool `yaml:"wants_tool_round_of_first_test_run"`
	WantsToolRoundOfFirstSourceEdit   bool `yaml:"wants_tool_round_of_first_source_edit"`
	WantsPrematureFixBeforeRedTestFlg bool `yaml:"wants_premature_fix_before_red_test_flag"`
}

// metricYAMLKeys is the closed set of keys a manifest's metrics block may
// carry, derived from metricsSpec's field tags so the set cannot drift from
// the struct. Anything else fails at load time instead of silently dropping
// into a map nobody reads.
var metricYAMLKeys = func() []string {
	t := reflect.TypeFor[metricsSpec]()
	keys := make([]string, 0, t.NumField())
	for f := range t.Fields() {
		if key, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}()

// UnmarshalYAML decodes the metrics block strictly: unknown keys are a
// load-time error so a typo'd metric name can never parse into dead state.
func (m *metricsSpec) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	for key := range raw {
		if !slices.Contains(metricYAMLKeys, key) {
			return fmt.Errorf("unknown metric %q (known: %s)", key, strings.Join(metricYAMLKeys, ", "))
		}
	}
	type plain metricsSpec
	return node.Decode((*plain)(m))
}

type fixtureSpec struct {
	Files map[string]string `yaml:"files"`
	// Git makes the work directory a repository on branch main whose first
	// commit holds Files, so a task can check what the agent changed.
	Git bool `yaml:"git,omitempty"`
	// Untracked files are written after that commit, so a task can check
	// that the agent leaves unrelated work alone.
	Untracked map[string]string `yaml:"untracked,omitempty"`
}

type expectSpec struct {
	Calls          []expectedCall   `yaml:"calls"`
	ForbiddenCalls []string         `yaml:"forbidden_calls"`
	Artifacts      []artifactExpect `yaml:"artifacts"`
	FinalContains  []string         `yaml:"final_contains"`
	// MaxCalls caps how many times a tool may be called, by canonical name.
	MaxCalls map[string]int `yaml:"max_calls,omitempty"`
	// Checks are shell commands run in the work directory after the agent
	// finishes. Each must exit zero. They judge the outcome of the work.
	Checks []checkSpec `yaml:"checks,omitempty"`
	// AllowToolErrors keeps tool errors from failing the run. A realistic
	// task meets missing files and failing commands on its way to the outcome.
	AllowToolErrors bool `yaml:"allow_tool_errors,omitempty"`
}

// checkSpec is one outcome check: a named shell command.
type checkSpec struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
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
	model              string
	fastCheapModel     string
	harness            string
	probesDir          string
	probeFilter        string
	outDir             string
	evenerBin          string
	systemPromptAppend []string
	build              bool
	repetitions        int
	maxRounds          int
	timeout            time.Duration
	postTurnWait       time.Duration
	reasoningEffort    string
	clearOpenAIAPIKey  bool
	sandbox            string
	sandboxNet         string
	inheritOperatorEnv bool // --inherit-operator-env: debugging escape hatch, restores the operator's real skills and plugins (#3227)
}

// holdsResults reports whether dir exists with anything in it. A run reuses
// its directories, so a second run into one that holds results would mix the
// two: the old work tree, the old sessions, and the old counts.
func holdsResults(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// defaultMaxRounds is the runner's round cap when --max-rounds is not given.
// It preserves the limit the runner hardcoded before the flag existed (#187).
const defaultMaxRounds = 80

func runSuite(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfg := runConfig{}
	// The repeatable flag's storage must outlive defineRunFlags: Set appends
	// through this pointer during Parse, so a slice returned by value before
	// parsing would keep the pre-Parse (empty) header and drop every value.
	var systemPromptAppend cmdutil.StringSliceFlag
	defineRunFlags(fs, &cfg, &systemPromptAppend)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.systemPromptAppend = []string(systemPromptAppend)
	// Decide the process-wide hermetic setting once, right after parsing and
	// before any probe runs (#3227). See configureHermeticRunEnv.
	configureHermeticRunEnv(cfg.inheritOperatorEnv)
	return runSuiteWithConfig(cfg)
}

func defineRunFlags(fs *flag.FlagSet, cfg *runConfig, systemPromptAppend *cmdutil.StringSliceFlag) {
	fs.StringVar(&cfg.model, "model", defaultModel(), "provider/model")
	fs.StringVar(&cfg.fastCheapModel, "fast-cheap-model", "", "provider/model or bare model for Evener auxiliary side calls")
	fs.StringVar(&cfg.harness, "harness", "cli", "execution harness: cli or live")
	fs.StringVar(&cfg.probesDir, "probes-dir", "tools/tool-fluency/probes", "probe manifest directory")
	fs.StringVar(&cfg.probeFilter, "probe", "all", "probe id or all")
	fs.StringVar(&cfg.outDir, "out", "", "result directory")
	fs.StringVar(&cfg.evenerBin, "evener-bin", "", "evener binary to run")
	fs.Var(systemPromptAppend, "system-prompt-append", "path to append to system prompt (repeatable)")
	fs.BoolVar(&cfg.build, "build", false, "build a fresh evener binary before running")
	fs.IntVar(&cfg.repetitions, "repetitions", 1, "repetitions per probe")
	fs.IntVar(&cfg.maxRounds, "max-rounds", defaultMaxRounds, "tool rounds the probe session may run per input (passed to evener)")
	fs.DurationVar(&cfg.timeout, "timeout", 8*time.Minute, "timeout per probe repetition")
	fs.DurationVar(&cfg.postTurnWait, "post-turn-wait", 45*time.Second, "live harness post-root-turn wait window")
	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "high", "reasoning effort")
	fs.BoolVar(&cfg.clearOpenAIAPIKey, "clear-openai-api-key", false, "clear "+envvars.OpenAIAPIKey.Name+" for OAuth-backed OpenAI runs")
	fs.StringVar(&cfg.sandbox, "sandbox", "off", "sandbox `mode`: off (default), read-only, workspace-write, or restricted (applies to both harnesses)")
	fs.StringVar(&cfg.sandboxNet, "sandbox-net", "on", "sandbox network egress `on|off` (default on; only applies with a non-off --sandbox mode)")
	fs.BoolVar(&cfg.inheritOperatorEnv, "inherit-operator-env", false, "debugging only: restore the operator's real skills and plugins instead of running hermetic (#3227)")
}

// enabledPluginsFlagLine matches the flag's own definition line in evener's
// --help output, not a mention of it in another flag's description.
var enabledPluginsFlagLine = regexp.MustCompile(`(?m)^\s+--enabled-plugins(\s|$)`)

// evenerHelpListsEnabledPlugins reports whether the evener at bin accepts
// --enabled-plugins, which a hermetic CLI run needs to hide the operator's
// plugins (#3227). Evener gained the flag on 2026-08-25, so a matrix version
// older than that lacks it. A --help that fails or times out proves nothing
// either way, so it is an error.
func evenerHelpListsEnabledPlugins(bin string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("run %s --help: %w", bin, err)
	}
	return enabledPluginsFlagLine.Match(out), nil
}

// evenerSupportsEnabledPlugins is evenerHelpListsEnabledPlugins behind a seam:
// the tests' fake evener scripts print one line for every invocation.
var evenerSupportsEnabledPlugins = evenerHelpListsEnabledPlugins

// requireHermeticEvener refuses an evener that cannot run hermetic, and names
// the way to run it anyway.
func requireHermeticEvener(bin string) error {
	ok, err := evenerSupportsEnabledPlugins(bin)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s predates --enabled-plugins, so a hermetic run cannot hide the operator's plugins from it; pass --inherit-operator-env to run it with them", bin)
	}
	return nil
}

func runSuiteWithConfig(cfg runConfig) error {
	if cfg.repetitions < 1 {
		return errors.New("--repetitions must be >= 1")
	}
	if cfg.maxRounds < 1 {
		return errors.New("--max-rounds must be >= 1")
	}
	if cfg.harness != "cli" && cfg.harness != "live" {
		return errors.New("--harness must be cli or live")
	}
	// Validate the sandbox flags once, up front, so a typo fails before any
	// probe launches instead of surfacing late from the spawned evener.
	if _, _, err := parseSandboxFlags(cfg.sandbox, cfg.sandboxNet); err != nil {
		return err
	}
	if cfg.outDir == "" {
		cfg.outDir = filepath.Join("tools", "tool-fluency", "results", time.Now().UTC().Format("20060102T150405Z"))
	}
	if holdsResults(cfg.outDir) {
		return fmt.Errorf("--out %s already holds results; a second run there would mix with them, so name a new directory", cfg.outDir)
	}
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return err
	}
	if cfg.harness == "cli" && (cfg.build || cfg.evenerBin == "") {
		bin, err := buildEvener(cfg.outDir)
		if err != nil {
			return err
		}
		cfg.evenerBin = bin
	}
	if cfg.harness == "cli" && !cfg.inheritOperatorEnv {
		if err := requireHermeticEvener(cfg.evenerBin); err != nil {
			return err
		}
	}
	probes, err := loadProbes(cfg.probesDir, cfg.probeFilter)
	if err != nil {
		return err
	}
	if len(probes) == 0 {
		return errors.New("no probes selected")
	}
	// Only the CLI harness's cliProbeArgs wires --ask-responder from a
	// person: block; the live harness (runLiveProbe) never does, so a
	// person-carrying task under --harness live would silently never ask.
	// Fail up front rather than run the whole suite for nothing to answer.
	for _, probe := range probes {
		if cfg.harness == "live" && probe.Person != nil {
			return fmt.Errorf("probe %q has a person: block, which --harness live does not support (it never asks); use --harness cli", probe.ID)
		}
	}
	// Report the actual selected set, honestly, before any live request is
	// launched (including the catalog session below). "--probe all" always
	// selects every probe under --probes-dir; this makes that scope visible
	// so a scoped request never silently balloons into the full set without
	// the caller seeing it named. See kata 73cb(a).
	fmt.Fprint(os.Stderr, selectionSummary(cfg, probes))
	reg, err := runnerLoadRegistry()
	if err != nil {
		return fmt.Errorf("provider registry: %w", err)
	}
	wireNames, err := wireNameToCanonicalForModel(reg, cfg.model)
	if err != nil {
		return err
	}
	catalog, err := catalogTools(reg, cfg.model)
	if err != nil {
		return err
	}
	available := make(map[string]bool, len(catalog))
	for _, tool := range catalog {
		available[tool.Name] = true
	}
	var results []probeResult
	for _, probe := range probes {
		for rep := 1; rep <= cfg.repetitions; rep++ {
			res := runProbe(cfg, probe, rep, available, wireNames)
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
	if v := envvars.EVENERFluencyModel.Trimmed(); v != "" {
		return v
	}
	if v := envvars.EVENERModel.Trimmed(); v != "" {
		return v
	}
	return "openai/gpt-5.4-mini"
}

func buildEvener(outDir string) (string, error) {
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	bin := filepath.Join(binDir, "evener")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/evener")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build evener: %w", err)
	}
	return bin, nil
}

// decodeProbe decodes one probe manifest strictly: an unknown field is an
// error, so a misspelled field cannot silently turn a check off. An empty
// manifest decodes to an empty probe, which loadProbes reports as missing its id.
func decodeProbe(data []byte) (probeFile, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var probe probeFile
	if err := dec.Decode(&probe); err != nil && !errors.Is(err, io.EOF) {
		return probeFile{}, err
	}
	return probe, nil
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
		probe, err := decodeProbe(data)
		if err != nil {
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

// selectionSummary reports exactly which probes "--probe" selected and from
// where, so a caller who scoped a request (e.g. to a subset of probes) never
// silently gets a broader set than they asked for without seeing it named.
// "all" always means every probe under --probes-dir; this makes that source
// set and its size explicit rather than changing what "all" means.
func selectionSummary(cfg runConfig, probes []probeFile) string {
	ids := make([]string, 0, len(probes))
	for _, p := range probes {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return fmt.Sprintf("[evener-fluency] model=%s probe-filter=%q probes-dir=%s selected=%d out=%s\n  probes: %s\n",
		cfg.model, cfg.probeFilter, cfg.probesDir, len(ids), cfg.outDir, strings.Join(ids, ", "))
}

type probeResult struct {
	Schema              int            `json:"schema"`
	Probe               string         `json:"probe"`
	Tool                string         `json:"tool,omitempty"`
	Model               string         `json:"model"`
	FastCheapModel      string         `json:"fast_cheap_model,omitempty"`
	Repetition          int            `json:"repetition"`
	Status              string         `json:"status"`
	EnvMode             string         `json:"env_mode"`
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
	Metrics             map[string]any `json:"metrics,omitempty"`
	Findings            []finding      `json:"findings"`
	DurationMS          int64          `json:"duration_ms"`
	Error               string         `json:"error,omitempty"`
	// AskUserCalls is how many times the session called ask_user (only set
	// for a task with a person: block); Asks is every question/answer pair
	// the --ask-responder logged (readAskLog, applyAskExchanges).
	AskUserCalls int           `json:"ask_user_calls,omitempty"`
	Asks         []askExchange `json:"asks,omitempty"`
}

// probeMetrics holds the phase-discipline metrics the runner computes from
// a run's transcripts (#187). They exist so a caller comparing prompting
// arms reads them out of result.json instead of hand-reading transcripts.
// Rounds are 1-based tool rounds of the ROOT session (assistant turns with
// at least one tool call; delegate/subagent sessions are excluded); zero
// means "never".
type probeMetrics struct {
	InvestigativeCallCount    int
	RepeatedReadOrGrepCount   int
	FirstTestRunRound         int
	FirstSourceEditRound      int
	PrematureFixBeforeRedTest bool
}

// wireNameToCanonicalForModel derives the wire-to-canonical tool-name
// inverse from the provider profile the run uses, so wire-name resolution
// stays in sync with provider renaming by construction: transcripts
// persist the names the provider emitted, and the profile is the same
// source of truth the renderer renamed tools with. A hand-maintained copy
// here would silently misclassify metrics the day a profile gains or
// changes a rename (roborev Medium on f5e1017).
func wireNameToCanonicalForModel(reg *registry.Registry, modelRef string) (map[string]string, error) {
	profile, err := provider.Resolve(reg, modelRef)
	if err != nil {
		return nil, err
	}
	var inverse map[string]string
	for canonical, wire := range profile.ToolNameMap() {
		if wire == canonical {
			continue
		}
		if inverse == nil {
			inverse = make(map[string]string)
		}
		inverse[wire] = canonical
	}
	return inverse, nil
}

// canonicalToolCallName resolves a transcript tool-call name to its
// canonical registry name using the run's profile-derived wire map.
// Already-canonical and unknown names pass through.
func canonicalToolCallName(name string, wireNames map[string]string) string {
	if canonical, ok := wireNames[name]; ok {
		return canonical
	}
	return name
}

// classifyToolCall classifies one canonical tool call for the phase metrics:
// investigative reads/searches, test runs, and source edits. Classification
// is structural (canonical tool name plus full argument JSON), never
// assistant prose.
func classifyToolCall(canonical, argsJSON string) (isInvestigative, isTestRun, isSourceEdit bool) {
	switch canonical {
	case "read_file", "list_dir", "grep", "glob":
		return true, false, false
	case "write_file", "edit_file", "apply_patch":
		return false, false, true
	case "shell":
		return false, isTestCommand(argsJSON), false
	default:
		return false, false, false
	}
}

// isTestCommand reports whether a shell call runs a test suite. It inspects
// the full raw JSON arguments object (never the truncated preview, and never
// assistant prose — a textual "go test" mention in prose does not reach this
// function). A `cd pkg && go test ...` compound or `FOO=1 go test ...`
// env-prefixed invocation counts, with quoted assignment values
// (`GOFLAGS='-mod=mod -race' go test ...`) stripped whole; `go vet` does not
// count (tests only). Only `&&`-joined compounds are split: a test command
// joined with `;` or `||` counts only when it starts its own segment.
var leadingEnvAssignment = regexp.MustCompile(`^[\w.]+=(?:"[^"]*"|'[^']*'|[^\s"']*)\s+`)

func isTestCommand(argsJSON string) bool {
	decoded := struct {
		Command string `json:"command"`
	}{}
	if err := json.Unmarshal([]byte(argsJSON), &decoded); err != nil {
		return false
	}
	cmd := decoded.Command
	for segment := range strings.SplitSeq(cmd, "&&") {
		segment = strings.TrimSpace(segment)
		// Strip leading env VAR=VALUE assignments, quoted values included,
		// so a value with spaces consumes its quotes instead of breaking the
		// line at the first space inside them.
		for {
			loc := leadingEnvAssignment.FindStringIndex(segment)
			if len(loc) != 2 || loc[0] != 0 {
				break
			}
			segment = strings.TrimSpace(segment[loc[1]:])
		}
		if segment == "go test" || strings.HasPrefix(segment, "go test ") {
			return true
		}
	}
	return false
}

// computeProbeMetrics walks the ROOT session's transcript (delegate and
// subagent sessions share the state dir but run their own rounds, so they are
// excluded the same way rootSessionID excludes them) and computes the
// phase-discipline metrics defined on probeMetrics.
func computeProbeMetrics(stateDir string, wireNames map[string]string) (probeMetrics, error) {
	var m probeMetrics
	rootID, err := rootSessionID(stateDir)
	if err != nil {
		// No root meta means no session ran (e.g. a skipped probe): there is
		// nothing to compute, which is not an error. The zero-valued metrics
		// say "never" for every round, exactly as a no-run should.
		return m, nil //nolint:nilerr // absent root session is a classified no-run state, not a metrics failure
	}
	seenReadTargets := map[string]bool{}
	tr, err := runnerReadTranscript(stateDir, rootID, doctor.TranscriptOpts{})
	if err != nil {
		return m, err
	}
	round := 0
	for _, turn := range tr.Turns {
		if turn.Kind != string(schema.TurnAssistant) || len(turn.ToolCalls) == 0 {
			continue
		}
		round++
		for _, call := range turn.ToolCalls {
			canonical := canonicalToolCallName(call.Name, wireNames)
			isInvestigative, isTestRun, isSourceEdit := classifyToolCall(canonical, call.Arguments)
			if isInvestigative {
				m.InvestigativeCallCount++
				if key := readRepeatKey(canonical, call.Arguments); key != "" {
					if seenReadTargets[key] {
						m.RepeatedReadOrGrepCount++
					}
					seenReadTargets[key] = true
				}
			}
			if isTestRun && m.FirstTestRunRound == 0 {
				m.FirstTestRunRound = round
			}
			if isSourceEdit && m.FirstSourceEditRound == 0 {
				m.FirstSourceEditRound = round
			}
		}
	}
	// Premature fix: the first source edit landed before the first test run
	// — including the never-ran-a-test case. The RED-then-fix discipline the
	// metric measures is violated either way.
	m.PrematureFixBeforeRedTest = m.FirstSourceEditRound > 0 &&
		(m.FirstTestRunRound == 0 || m.FirstSourceEditRound < m.FirstTestRunRound)
	return m, nil
}

// readRepeatKey builds the repeat-tracking key for an investigative call:
// the canonical tool shape plus the discriminating argument values (path or
// pattern, and offset where the tool takes one). Two calls count as a
// repeat only when they read the same thing the same way — a read_file of
// a.go is not a repeat of a list_dir of a.go, and page 2 of a file is not a
// repeat of page 1.
func readRepeatKey(canonical, argsJSON string) string {
	var decoded struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		Offset   int    `json:"offset"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &decoded); err != nil {
		return ""
	}
	var target string
	switch canonical {
	case "grep":
		target = decoded.Pattern + "\x00" + decoded.Path
	case "glob":
		target = decoded.Pattern
	default:
		if decoded.FilePath != "" {
			target = decoded.FilePath
		} else {
			target = decoded.Path
		}
	}
	if target == "" {
		return ""
	}
	return canonical + "\x00" + target + "\x00" + strconv.Itoa(decoded.Offset)
}

// applyProbeMetrics computes the phase metrics for a run and reports on the
// result exactly the metrics the probe's manifest asked for. Unrequested
// metrics stay unreported so a result reflects its probe's declared scope.
// A transcript that cannot be read is surfaced as a finding (the run may
// still pass its expectations, but the missing metrics are never silently
// dropped).
func applyProbeMetrics(res *probeResult, probe probeFile, stateDir string, wireNames map[string]string) {
	m, err := computeProbeMetrics(stateDir, wireNames)
	if err != nil {
		res.Findings = append(res.Findings, finding{
			Category: "infra",
			Title:    "phase metrics unavailable",
			Detail:   err.Error(),
		})
		return
	}
	if res.Metrics == nil {
		res.Metrics = map[string]any{}
	}
	if probe.Metrics.WantsInvestigativeCallCount {
		res.Metrics["investigative_call_count"] = m.InvestigativeCallCount
	}
	if probe.Metrics.WantsRepeatedReadOrGrepCount {
		res.Metrics["repeated_read_or_grep_count"] = m.RepeatedReadOrGrepCount
	}
	if probe.Metrics.WantsToolRoundOfFirstTestRun {
		res.Metrics["tool_round_of_first_test_run"] = m.FirstTestRunRound
	}
	if probe.Metrics.WantsToolRoundOfFirstSourceEdit {
		res.Metrics["tool_round_of_first_source_edit"] = m.FirstSourceEditRound
	}
	if probe.Metrics.WantsPrematureFixBeforeRedTestFlg {
		res.Metrics["premature_fix_before_red_test_flag"] = m.PrematureFixBeforeRedTest
	}
	// max_tool_calls is the manifest's budget for total tool calls across
	// every session transcript in the state dir — delegate and subagent
	// sessions included, the same walk allTranscriptToolCounts performs
	// (#187: it was previously parsed and ignored). Delegation is churn too,
	// so a probe that blows its budget through child sessions still flags.
	// Exceeding it is churn, not task failure: a finding, so the run reports
	// the overage instead of silently passing.
	if probe.Metrics.MaxToolCalls > 0 {
		total := 0
		for _, n := range res.ModelToolCounts {
			total += n
		}
		if total > probe.Metrics.MaxToolCalls {
			res.Findings = append(res.Findings, finding{
				Category: "churn",
				Title:    "tool call budget exceeded",
				Detail:   fmt.Sprintf("calls=%d budget=%d", total, probe.Metrics.MaxToolCalls),
			})
		}
	}
}

type finding struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

// envModeLabel names, for result.json, which environment mode a probe ran
// under (#3227): "hermetic" (today's default, operator skills and plugins
// hidden) or "inherit_operator_env" (--inherit-operator-env, debugging only).
func envModeLabel(inheritOperatorEnv bool) string {
	if inheritOperatorEnv {
		return "inherit_operator_env"
	}
	return "hermetic"
}

func runProbe(cfg runConfig, probe probeFile, rep int, available map[string]bool, wireNames map[string]string) probeResult {
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
		EnvMode:             envModeLabel(cfg.inheritOperatorEnv),
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
	// timeout <= 0 means no per-probe deadline: deterministic offline tests
	// run under go test's own -timeout as the hang backstop instead of a
	// wall-clock budget that load spikes can breach (supersedes the
	// 2026-07-13 execenv-fixtures record's per-probe guard; kata 73cb).
	ctx := context.Background()
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}
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
	}
	if counts, err := allTranscriptToolCounts(stateDir); err == nil {
		res.ModelToolCounts = counts
	}
	// Phase-discipline metrics (#187): read from the root session's transcript
	// rather than through the enumeration the counts above walk, and reported
	// per the probe's metrics block.
	applyProbeMetrics(&res, probe, stateDir, wireNames)
	applyAskExchanges(&res, probe)
	if err != nil {
		res.Error = err.Error()
		category, status := classifyProbeError(err, ctx.Err(), stderr.String())
		if status != "" {
			res.Status = status
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
	args, err := cliProbeArgs(cfg, probe, res)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, cfg.evenerBin, args...)
	cmd.Env = fixtureEnv(res.WorkDir)
	if cfg.clearOpenAIAPIKey {
		cmd.Env = append(cmd.Env, envvars.OpenAIAPIKey.Assignment(""))
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func cliProbeArgs(cfg runConfig, probe probeFile, res probeResult) ([]string, error) {
	args := []string{"--model", cfg.model}
	if strings.TrimSpace(cfg.fastCheapModel) != "" {
		args = append(args, "--fast-cheap-model", cfg.fastCheapModel)
	}
	for _, path := range cfg.systemPromptAppend {
		if strings.TrimSpace(path) != "" {
			args = append(args, "--system-prompt-append", path)
		}
	}
	// A declared sandbox mode reaches the spawned evener too, so the CLI harness
	// confines its worker exactly as the live harness does. Off forwards nothing,
	// leaving today's CLI runs byte-identical. An invalid mode/net returns the
	// error rather than silently dropping the flags and running the worker
	// unsandboxed, so a caller that bypasses runSuiteWithConfig fails closed.
	mode, net, err := parseSandboxFlags(cfg.sandbox, cfg.sandboxNet)
	if err != nil {
		return nil, err
	}
	if mode != sandbox.ModeOff {
		netName := "on"
		if !net {
			netName = "off"
		}
		args = append(args, "--sandbox", mode.String(), "--sandbox-net", netName)
	}
	if probe.Person != nil {
		askResponder, err := personAskResponderCommand(cfg, probe, res)
		if err != nil {
			return nil, err
		}
		args = append(args, "--ask-responder", askResponder)
	}
	// EVENER_NO_USER_SKILLS (configureHermeticRunEnv) hides only the operator's
	// home and user-config skills. Their installed, enabled plugins would still
	// load in full — hooks, agents, commands, and plugin-sourced skills —
	// because a bare `evener run` resolves plugins against its default root
	// (internal/plugins.ResolveForLaunch with no explicit selection). An
	// explicit empty selection makes that resolution pick nothing, whatever the
	// operator has installed (#3227). --inherit-operator-env restores today's
	// behavior for debugging by omitting the flag entirely.
	if !cfg.inheritOperatorEnv {
		args = append(args, "--enabled-plugins", "")
	}
	args = append(args,
		"--dir", res.WorkDir,
		"--state-dir", res.StateDir,
		"--reasoning-effort", cfg.reasoningEffort,
		"--context-strategy", "compact",
		"--max-rounds", strconv.Itoa(cfg.maxRounds),
		"--verbose",
		probe.Prompt,
	)
	return args, nil
}

type liveKick struct {
	kind  agent.EntryKind
	input string
}

var runnerLoadClient = cmdutil.LoadClient
var runnerAttachAPILogger = cmdutil.AttachAPILogger
var runnerMarshalEvent = json.Marshal
var runnerProbeSandboxHost = func() sandbox.HostFacts { return sandbox.RealProber{}.Probe() }

// parseSandboxFlags validates the runner's --sandbox / --sandbox-net values once,
// for both harnesses, and returns the normalized wire mode and the resolved
// network decision. Off (empty or "off") is mode off; the net value parses with
// empty defaulting to on. An unknown mode or net value is a legible error rather
// than a silent native run.
//
// --sandbox-net is validated even when the mode is off, matching the production
// CLI (cmd/evener configureSandbox parses the net flag before its off
// short-circuit); the net decision is only APPLIED when the mode is non-off.
func parseSandboxFlags(modeName, netName string) (sandbox.Mode, bool, error) {
	name := strings.TrimSpace(modeName)
	if name == "" {
		name = sandbox.ModeOff.String()
	}
	mode, err := sandbox.ParseMode(name)
	if err != nil {
		return sandbox.ModeOff, false, err
	}
	net, err := parseLiveSandboxNet(netName)
	if err != nil {
		return sandbox.ModeOff, false, err
	}
	return mode, net, nil
}

// configureLiveSandbox records the validated --sandbox / --sandbox-net decision
// on the session config's carrier fields. Off (the flag default) leaves the
// carrier zero, so a default live run is byte-identical to today; a non-off mode
// records the mode + network so provisionLiveSandbox can enforce it.
func configureLiveSandbox(cfg runConfig, sessCfg *agent.SessionConfig) error {
	mode, net, err := parseSandboxFlags(cfg.sandbox, cfg.sandboxNet)
	if err != nil {
		return err
	}
	if mode == sandbox.ModeOff {
		return nil
	}
	sessCfg.Sandbox = mode.String()
	sessCfg.SandboxNet = &net
	return nil
}

// parseLiveSandboxNet maps the --sandbox-net value to a boolean (on = egress
// allowed). Empty defaults to on, matching the CLI's flag default.
func parseLiveSandboxNet(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid --sandbox-net %q (want on or off)", v)
	}
}

// provisionLiveSandbox engages enforcement on a fresh live run's execution
// environment from its configured mode. Off returns immediately WITHOUT probing
// the host, so an unsandboxed run stays byte-identical to today. It reuses the
// existing sandbox capability (resolve against freshly-probed host facts, then
// EnableSandbox) exactly as the production CLI does.
func provisionLiveSandbox(env *execenv.LocalExecutionEnvironment, sessCfg *agent.SessionConfig, cwd string) error {
	if sandbox.ModeIsOff(sessCfg.Sandbox) {
		return nil
	}
	return provisionLiveSandboxWithHost(env, sessCfg, cwd, runnerProbeSandboxHost())
}

// provisionLiveSandboxWithHost is provisionLiveSandbox with the host facts
// supplied by the caller, so a test can drive the exact resolve+enforce path
// without a live backend. It fails closed: a mode the host cannot enforce
// surfaces the resolver's *sandbox.RefusalError BEFORE any session or provider
// call, and EnableSandbox leaves the env unsandboxed on every failure path.
func provisionLiveSandboxWithHost(env *execenv.LocalExecutionEnvironment, sessCfg *agent.SessionConfig, cwd string, host sandbox.HostFacts) error {
	infra := agent.SessionInfraRoots(*sessCfg, env)
	rp, err := sandbox.ResolveNamed(sessCfg.Sandbox, sessCfg.SandboxNet, host, cwd, infra)
	if err != nil {
		return err
	}
	return env.EnableSandbox(rp)
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
	client, err := runnerLoadClient(res.StateDir)
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}
	closeAPILog, err := runnerAttachAPILogger(client, res.StateDir, stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck

	profile, err := runnerInitialProfile(client, modelRef)
	if err != nil {
		return err
	}
	profile, err = runnerApplyFastCheapModel(profile, cfg.fastCheapModel, client)
	if err != nil {
		return err
	}
	effort, err := cmdutil.ResolveReasoningEffort(cfg.reasoningEffort, envvars.EVENERReasoningEffort.Getenv())
	if err != nil {
		return err
	}

	sessCfg := buildLiveSessionConfig(cfg, res.StateDir)
	sessCfg.ResolveProfile = cmdutil.BuildResolveProfile(client)
	if effort.Set {
		sessCfg.ReasoningEffort = effort.Value
	}
	if err := configureLiveSandbox(cfg, &sessCfg); err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(res.WorkDir)
	// Engage enforcement before the session exists so a declared mode the host
	// cannot serve fails closed here, before any provider call, rather than
	// silently running the worker native and unsandboxed.
	if err := provisionLiveSandbox(env, &sessCfg, res.WorkDir); err != nil {
		return err
	}
	sess, err := runnerNewSession(client, profile, env, sessCfg)
	if err != nil {
		// The session that would have owned whatever this env provisioned was
		// never built; settle its scratch against the root retention manifest
		// the way the production launch path does, rather than removing a
		// directory a durable manifest may still reference.
		agent.DisposeRootScratchAfterFailure(sessCfg.StateDir, env)
		return err
	}
	res.SessionID = sess.ID()

	var stderrMu sync.Mutex
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			line, err := runnerMarshalEvent(ev)
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
	sess.SetKickFunc(liveKickSubmitter(kicks))
	sess.SetNotifyFunc(liveNotifySubmitter(kicks))

	closeSession := func() {
		sess.Close()
		<-eventsDone
	}
	defer closeSession()

	out, err := sess.ProcessInput(ctx, probe.Prompt, nil)
	appendLiveOutput(stdout, out)
	if err != nil {
		return err
	}
	if cfg.postTurnWait <= 0 {
		return nil
	}

	timer := time.NewTimer(cfg.postTurnWait)
	defer timer.Stop()
	return runLiveKickLoop(ctx, timer.C, kicks, sess, stdout)
}

// buildLiveSessionConfig is the live harness's session-config seam. The
// round cap derives from the runner's --max-rounds flag, not a hardcoded
// value, so the documented flag is authoritative in both harnesses (#187).
func buildLiveSessionConfig(cfg runConfig, stateDir string) agent.SessionConfig {
	return agent.SessionConfig{
		MaxToolRoundsPerInput: cmdutil.MaxRoundsToConfig(cfg.maxRounds),
		StateDir:              stateDir,
		SystemPromptAppend:    cfg.systemPromptAppend,
		NonInteractive:        true,
		ContextStrategy:       "compact",
	}
}

func trySubmitLiveKick(kicks chan<- liveKick, kick liveKick) {
	select {
	case kicks <- kick:
	default:
	}
}

func liveKickSubmitter(kicks chan<- liveKick) func(string) {
	return func(prompt string) {
		trySubmitLiveKick(kicks, liveKick{kind: agent.EntryContinuation, input: prompt})
	}
}

func liveNotifySubmitter(kicks chan<- liveKick) func() {
	return func() {
		trySubmitLiveKick(kicks, liveKick{kind: agent.EntryNotification})
	}
}

func appendLiveOutput(stdout *bytes.Buffer, out string) {
	if strings.TrimSpace(out) != "" {
		stdout.WriteString(out)
		stdout.WriteByte('\n')
	}
}

type liveKickProcessor interface {
	ProcessInputKind(context.Context, string, []agent.ImageAttachment, agent.EntryKind) (string, error)
}

func runLiveKickLoop(ctx context.Context, timer <-chan time.Time, kicks <-chan liveKick, process liveKickProcessor, stdout *bytes.Buffer) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer:
			return nil
		case kick := <-kicks:
			out, err := process.ProcessInputKind(ctx, kick.input, nil, kick.kind)
			appendLiveOutput(stdout, out)
			if err != nil {
				return err
			}
		}
	}
}

func maybeClearOpenAIAPIKey(shouldClear bool) func() {
	if !shouldClear {
		return func() {}
	}
	old, ok := envvars.OpenAIAPIKey.LookupEnv()
	_ = envvars.OpenAIAPIKey.Unsetenv()
	return func() {
		if ok {
			_ = envvars.OpenAIAPIKey.Setenv(old)
		} else {
			_ = envvars.OpenAIAPIKey.Unsetenv()
		}
	}
}

func runnerInitialProfile(client *llm.Client, modelRef cmdutil.ModelRef) (*provider.Profile, error) {
	raw, err := cmdutil.ResolveProfile(client, modelRef.Qualified())
	if err != nil {
		return nil, err
	}
	return provider.WithAllowedDecisions(raw, cmdutil.ParseAllowedDecisions(envvars.EVENERAllowedDecisions.Getenv())), nil
}

func runnerApplyFastCheapModel(profile *provider.Profile, raw string, client *llm.Client) (*provider.Profile, error) {
	if profile == nil || strings.TrimSpace(raw) == "" {
		return profile, nil
	}
	raw = strings.TrimSpace(raw)
	if cheapProvider, model, ok := strings.Cut(raw, "/"); ok && cheapProvider != "" && model != "" && cheapProvider != profile.ID() {
		if !client.HasProvider(cheapProvider) {
			return nil, fmt.Errorf("--fast-cheap-model provider %q is not configured or has no credential (active provider %q); available providers: %s",
				cheapProvider, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return provider.WithCheapModel(profile, raw), nil
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

// walkTranscripts calls fn with every session transcript under stateDir in
// session-file order, rendered with opts. It returns the first error from fn
// or from reading a transcript. The tool-count aggregation walks every
// transcript through this enumeration; the phase metrics read the root
// session's transcript directly.
func walkTranscripts(stateDir string, opts doctor.TranscriptOpts, fn func(doctor.TranscriptResult) error) error {
	matches, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.transcript.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	for _, path := range matches {
		id, ok := strings.CutSuffix(filepath.Base(path), ".transcript.jsonl")
		if !ok || id == "" {
			continue
		}
		tr, err := runnerReadTranscript(stateDir, id, opts)
		if err != nil {
			return err
		}
		if err := fn(tr); err != nil {
			return err
		}
	}
	return nil
}

func materializeFixture(workDir string, fixture fixtureSpec) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	if err := writeFixtureFiles(workDir, fixture.Files); err != nil {
		return err
	}
	if fixture.Git {
		if err := commitFixture(workDir); err != nil {
			return err
		}
	}
	return writeFixtureFiles(workDir, fixture.Untracked)
}

// writeFixtureFiles writes each file under workDir and refuses a path that
// escapes it.
func writeFixtureFiles(workDir string, files map[string]string) error {
	for rel, content := range files {
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

// commitFixture makes workDir a repository on main with one commit holding
// everything written so far. The identity lives in the repository, so the
// agent's own commits work on a machine with no global identity. The user's
// global git setup belongs to their own work and stays out: the repository
// starts from no template, so no template hooks land in it; it never signs;
// it uses its own empty hooks directory; and it reads no global ignore file.
func commitFixture(workDir string) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "--template="},
		{"config", "user.name", "Evener Fixture"},
		{"config", "user.email", "fixture@evener.test"},
		{"config", "commit.gpgsign", "false"},
		{"config", "core.hooksPath", ".git/hooks"},
		{"config", "core.excludesFile", os.DevNull},
		{"add", "-A"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// fixtureEnv is the environment for anything that acts on a fixture: the
// agent's evener process and the task checks. It cuts the fixture off from any
// Go workspace or git repository above it, so results that land inside a
// repository, such as evener's own, still run each fixture as its own project.
func fixtureEnv(workDir string) []string {
	env := append(os.Environ(), "GOWORK=off")
	if parent, err := filepath.Abs(filepath.Dir(workDir)); err == nil {
		env = append(env, "GIT_CEILING_DIRECTORIES="+parent)
	}
	return env
}

func parseEvents(data []byte) (map[string]int, map[string]int, []string) {
	counts := map[string]int{}
	errorsByTool := map[string]int{}
	var communicateMessages []string
	for raw := range bytes.SplitSeq(data, []byte("\n")) {
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

func allTranscriptToolCounts(stateDir string) (map[string]int, error) {
	counts := map[string]int{}
	err := walkTranscripts(stateDir, doctor.TranscriptOpts{}, func(tr doctor.TranscriptResult) error {
		for tool, n := range transcriptToolCounts(tr) {
			counts[tool] += n
		}
		return nil
	})
	if err != nil {
		return counts, err
	}
	return counts, nil
}

var runnerReadTranscript = doctor.Transcript

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

// callCount is how many times the run called a tool. A manifest may name a
// tool by its canonical name or by the name the model saw, so the count is the
// larger of the two.
func callCount(res probeResult, name string) int {
	return max(res.CanonicalToolCounts[name], res.ModelToolCounts[name])
}

func evaluateExpectations(workDir string, probe probeFile, res probeResult) []finding {
	var out []finding
	for _, call := range probe.Expect.Calls {
		minCalls := call.Min
		if minCalls == 0 {
			minCalls = 1
		}
		got := callCount(res, call.Tool)
		if got < minCalls {
			out = append(out, finding{
				Category: "selection",
				Title:    "expected tool was not called enough",
				Detail:   fmt.Sprintf("%s got=%d want>=%d", call.Tool, got, minCalls),
			})
		}
	}
	for _, name := range probe.Expect.ForbiddenCalls {
		got := callCount(res, name)
		if got > 0 {
			out = append(out, finding{
				Category: "churn",
				Title:    "forbidden tool was called",
				Detail:   fmt.Sprintf("%s calls=%d", name, got),
			})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(probe.Expect.MaxCalls)) {
		limit := probe.Expect.MaxCalls[name]
		got := callCount(res, name)
		if got > limit {
			out = append(out, finding{
				Category: "churn",
				Title:    "tool called more often than the task allows",
				Detail:   fmt.Sprintf("%s calls=%d max=%d", name, got, limit),
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
	for _, check := range probe.Expect.Checks {
		if ok, detail := runCheck(workDir, check, checkTimeout); !ok {
			out = append(out, finding{
				Category: "outcome",
				Title:    "check failed: " + check.Name,
				Detail:   detail,
			})
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
	if !probe.Expect.AllowToolErrors {
		for toolName, n := range res.ToolErrors {
			if n > 0 {
				out = append(out, finding{
					Category: "arguments",
					Title:    "tool returned validation/runtime errors",
					Detail:   fmt.Sprintf("%s errors=%d", toolName, n),
				})
			}
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

// checkTimeout bounds one outcome check, so a command that waits on a
// terminal or on a child process cannot stall a run.
const checkTimeout = 2 * time.Minute

// runCheck runs one check in workDir with no stdin. It reports whether the
// check passed and, when it failed, why.
func runCheck(workDir string, check checkSpec, timeout time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", check.Run)
	cmd.Dir = workDir
	cmd.Env = fixtureEnv(workDir)
	// The check runs in its own process group, so the kill at the deadline
	// reaches everything it started, such as a go test binary stuck in a loop.
	// The cost: a Ctrl-C of the runner no longer reaches a running check.
	cmd.SysProcAttr = procgroup.SysProcAttr()
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procgroup.Kill(cmd.Process.Pid)
		}
		return nil
	}
	// A descendant that left the group can still hold the output pipe; stop
	// waiting for it shortly after the kill.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, ""
	}
	if ctx.Err() != nil {
		return false, fmt.Sprintf("timed out after %s", timeout)
	}
	return false, fmt.Sprintf("%v: %s", err, lastBytes(strings.TrimSpace(string(out)), 1500))
}

// lastBytes keeps the end of s, where a failing command says why it failed.
// The cut point advances forward, never landing inside a multi-byte UTF-8
// rune (keeping at most n bytes, never more), the same rune-boundary
// technique agent/doctor.Truncate uses when it cuts from the front.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "..." + s[cut:]
}

// classifyProbeError decides whether a probe command failure reflects the
// model/tool under test, or a failure of the harness itself (subprocess
// spawn failures, timeout/plumbing errors, environment issues). Harness
// failures must never be attributed to the model in reporting.
func classifyProbeError(err error, ctxErr error, stderr string) (category, status string) {
	switch {
	case ctxErr != nil || looksInfraError(stderr):
		return "infra", "blocked_infra"
	case isHarnessSpawnError(err):
		return "harness", "blocked_harness"
	default:
		return "runtime", ""
	}
}

// isHarnessSpawnError reports whether err came from the harness failing to
// launch the probe subprocess at all (missing binary, permission denied,
// etc.), as opposed to the subprocess running and exiting with a failure
// that reflects the model/tool under test.
func isHarnessSpawnError(err error) bool {
	var execErr *exec.Error
	return errors.As(err, &execErr)
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
	data, err := runnerMarshalProbeResult(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	jsonl := filepath.Join(outDir, "results.jsonl")
	line, _ := json.Marshal(res)
	f, err := runnerOpenResultAppend(jsonl, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	return f.Close()
}

type resultAppendFile interface {
	Write([]byte) (int, error)
	Close() error
}

var runnerMarshalProbeResult = func(res probeResult, prefix, indent string) ([]byte, error) {
	return json.MarshalIndent(res, prefix, indent)
}

var runnerOpenResultAppend = func(name string, flag int, perm os.FileMode) (resultAppendFile, error) {
	return os.OpenFile(name, flag, perm)
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
	data, err := runnerMarshalSummary(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "summary.json"), append(data, '\n'), 0o644)
}

var runnerMarshalSummary = json.MarshalIndent

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
