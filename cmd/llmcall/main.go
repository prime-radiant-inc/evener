package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
)

// llmcall is a minimal single-call CLI for the unified llm client.
//
// Design constraints:
// - Exactly one LLM call (no agent loop).
// - No tool calls allowed (tool_choice=none; error if any are returned).
// - No system prompt by default; optional --system / --system-file / --system-append.
func main() {
	if err := llmcallMain(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "llmcall: %v\n", err)
		os.Exit(1)
	}
}

// stringSliceFlag implements flag.Value for a repeatable string flag.
type stringSliceFlag []string

func (f *stringSliceFlag) String() string { return strings.Join(*f, ",") }
func (f *stringSliceFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

type llmCallConfig struct {
	prompt   string
	model    string
	provider string

	// System prompt control. Default: no system prompt.
	systemText   string
	systemFile   string
	systemAppend []string
	noSystem     bool

	format string // "text"|"json"
	schema string // path to JSON schema file (enables json_schema)
	pretty bool

	temperature     float64
	topP            float64
	maxTokens       int
	reasoningEffort string
	stopSequences   []string
	webSearch       bool
	timeoutTotal    time.Duration
	verbose         bool

	metadata []string // key=value pairs

	stdout io.Writer
	stderr io.Writer

	// client is injectable for tests. If nil, one will be constructed from env.
	client *llm.Client
}

func llmcallMain(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("llmcall", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := llmCallConfig{
		stdout:      stdout,
		stderr:      stderr,
		format:      "text",
		temperature: -1,
		topP:        -1,
		maxTokens:   -1,
	}

	fs.StringVar(&cfg.provider, "provider", "", "LLM provider: openai, anthropic, google (or set LLM_PROVIDER / SERF_PROVIDER)")
	fs.StringVar(&cfg.model, "model", "", "LLM model identifier (or set LLM_MODEL / SERF_MODEL)")

	fs.StringVar(&cfg.systemText, "system", "", "system prompt text (optional)")
	fs.StringVar(&cfg.systemFile, "system-file", "", "path to a system prompt file (optional)")
	var systemAppend stringSliceFlag
	fs.Var(&systemAppend, "system-append", "path to append to system prompt (repeatable)")
	fs.BoolVar(&cfg.noSystem, "no-system", false, "force no system prompt (ignores --system/--system-file/--system-append)")

	fs.StringVar(&cfg.format, "format", "text", "output format: text|json")
	fs.StringVar(&cfg.schema, "schema", "", "path to JSON Schema (enables structured output and validation)")
	fs.BoolVar(&cfg.pretty, "pretty", false, "pretty-print JSON output (json/json_schema)")

	fs.Float64Var(&cfg.temperature, "temperature", -1, "sampling temperature (unset if <0)")
	fs.Float64Var(&cfg.topP, "top-p", -1, "top-p nucleus sampling (unset if <0)")
	fs.IntVar(&cfg.maxTokens, "max-tokens", -1, "max output tokens (unset if <0)")
	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "", "reasoning effort: low|medium|high|none")
	var stop stringSliceFlag
	fs.Var(&stop, "stop", "stop sequence (repeatable)")
	fs.BoolVar(&cfg.webSearch, "web-search", false, "enable provider-native web search")
	var timeoutStr string
	fs.StringVar(&timeoutStr, "timeout", "", "overall timeout (e.g. 30s, 2m)")
	fs.BoolVar(&cfg.verbose, "verbose", false, "emit basic request/usage info to stderr")

	var metadata stringSliceFlag
	fs.Var(&metadata, "meta", "metadata key=value (repeatable)")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage:\n")
		fmt.Fprintf(stderr, "  llmcall --provider <provider> --model <model> [flags] <prompt>\n")
		fmt.Fprintf(stderr, "  echo <prompt> | llmcall --provider <provider> --model <model> [flags]\n\n")
		fmt.Fprintf(stderr, "Notes:\n")
		fmt.Fprintf(stderr, "  - Single LLM call (no agent loop).\n")
		fmt.Fprintf(stderr, "  - Tool calls are forbidden (tool_choice=none).\n")
		fmt.Fprintf(stderr, "  - No system prompt by default.\n\n")
		fmt.Fprintf(stderr, "Required:\n")
		fmt.Fprintf(stderr, "  --provider <name>        openai|anthropic|google (or LLM_PROVIDER/SERF_PROVIDER)\n")
		fmt.Fprintf(stderr, "  --model <id>             model identifier (or LLM_MODEL/SERF_MODEL)\n")
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	cfg.prompt = strings.TrimSpace(strings.Join(fs.Args(), " "))
	if cfg.prompt == "" {
		// If no args, try reading from stdin (piped input).
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			b, err := io.ReadAll(os.Stdin)
			if err == nil {
				cfg.prompt = strings.TrimSpace(string(b))
			}
		}
	}
	if cfg.prompt == "" {
		fs.Usage()
		return fmt.Errorf("no prompt provided")
	}

	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("invalid --timeout %q: %w", timeoutStr, err)
		}
		cfg.timeoutTotal = d
	}

	// Resolve provider/model from env if omitted.
	if cfg.provider == "" {
		cfg.provider = os.Getenv("LLM_PROVIDER")
	}
	if cfg.provider == "" {
		cfg.provider = os.Getenv("SERF_PROVIDER")
	}
	if cfg.model == "" {
		cfg.model = os.Getenv("LLM_MODEL")
	}
	if cfg.model == "" {
		cfg.model = os.Getenv("SERF_MODEL")
	}
	if cfg.provider == "" {
		return fmt.Errorf("no provider specified: use --provider or set LLM_PROVIDER/SERF_PROVIDER")
	}
	if cfg.model == "" {
		return fmt.Errorf("no model specified: use --model or set LLM_MODEL/SERF_MODEL")
	}

	cfg.systemAppend = []string(systemAppend)
	cfg.stopSequences = []string(stop)
	cfg.metadata = []string(metadata)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	return runLLMCall(ctx, cfg)
}

func runLLMCall(ctx context.Context, cfg llmCallConfig) error {
	if cfg.stdout == nil {
		cfg.stdout = os.Stdout
	}
	if cfg.stderr == nil {
		cfg.stderr = os.Stderr
	}

	client := cfg.client
	if client == nil {
		c, err := llm.NewFromEnv()
		if err != nil {
			return fmt.Errorf("LLM client setup: %w", err)
		}
		client = c
	}

	system := ""
	if !cfg.noSystem {
		var err error
		system, err = buildSystemPrompt(cfg.systemText, cfg.systemFile, cfg.systemAppend)
		if err != nil {
			return err
		}
	}

	maxToolRounds := 0
	opts := llm.GenerateOptions{
		Client:        client,
		Model:         cfg.model,
		Provider:      cfg.provider,
		Prompt:        &cfg.prompt,
		Tools:         nil,
		ToolChoice:    &llm.ToolChoice{Mode: "none"},
		MaxToolRounds: &maxToolRounds,

		WebSearch:     cfg.webSearch,
		StopSequences: append([]string{}, cfg.stopSequences...),
		TimeoutTotal:  cfg.timeoutTotal,
	}
	if system != "" {
		opts.System = &system
	}
	if cfg.temperature >= 0 {
		opts.Temperature = &cfg.temperature
	}
	if cfg.topP >= 0 {
		opts.TopP = &cfg.topP
	}
	if cfg.maxTokens >= 0 {
		opts.MaxTokens = &cfg.maxTokens
	}
	if strings.TrimSpace(cfg.reasoningEffort) != "" {
		v := strings.TrimSpace(cfg.reasoningEffort)
		opts.ReasoningEffort = &v
	}
	if len(cfg.metadata) > 0 {
		m, err := parseMetadata(cfg.metadata)
		if err != nil {
			return err
		}
		opts.Metadata = m
	}

	if cfg.schema != "" {
		schema, err := readJSONSchemaFile(cfg.schema)
		if err != nil {
			return err
		}
		res, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
			GenerateOptions: opts,
			Schema:          schema,
		})
		if err != nil {
			return err
		}
		if len(res.ToolCalls) > 0 {
			return fmt.Errorf("tool calls are not allowed (got %d)", len(res.ToolCalls))
		}
		if cfg.verbose {
			printUsage(cfg.stderr, res)
		}
		return writeJSON(cfg.stdout, res.Output, cfg.pretty)
	}

	if cfg.format != "text" && cfg.format != "json" {
		return fmt.Errorf("invalid --format %q: must be text|json", cfg.format)
	}
	if cfg.format == "json" {
		opts.ResponseFormat = &llm.ResponseFormat{Type: "json"}
	}

	res, err := llm.Generate(ctx, opts)
	if err != nil {
		return err
	}
	if len(res.ToolCalls) > 0 {
		return fmt.Errorf("tool calls are not allowed (got %d)", len(res.ToolCalls))
	}
	if cfg.verbose {
		printUsage(cfg.stderr, res)
	}

	switch cfg.format {
	case "text":
		fmt.Fprintln(cfg.stdout, res.Text) //nolint:errcheck
		return nil
	case "json":
		var out any
		dec := json.NewDecoder(strings.NewReader(res.Text))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			return fmt.Errorf("failed to parse JSON output: %w", err)
		}
		return writeJSON(cfg.stdout, out, cfg.pretty)
	default:
		return fmt.Errorf("invalid --format %q: must be text|json", cfg.format)
	}
}

func buildSystemPrompt(systemText, systemFile string, appendFiles []string) (string, error) {
	if systemText != "" && systemFile != "" {
		return "", fmt.Errorf("provide only one of --system or --system-file")
	}

	base := strings.TrimSpace(systemText)
	if systemFile != "" {
		b, err := os.ReadFile(systemFile)
		if err != nil {
			return "", fmt.Errorf("read --system-file %s: %w", systemFile, err)
		}
		base = strings.TrimSpace(string(b))
	}

	if len(appendFiles) == 0 {
		return base, nil
	}
	var parts []string
	if base != "" {
		parts = append(parts, base)
	}
	for _, p := range appendFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read --system-append %s: %w", p, err)
		}
		s := strings.TrimSpace(string(b))
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func readJSONSchemaFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --schema %s: %w", path, err)
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse JSON schema %s: %w", path, err)
	}
	if out == nil {
		return nil, fmt.Errorf("parse JSON schema %s: empty object", path)
	}
	return out, nil
}

func parseMetadata(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range pairs {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid --meta %q: expected key=value", kv)
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out, nil
}

func writeJSON(w io.Writer, v any, pretty bool) error {
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func printUsage(w io.Writer, res *llm.GenerateResult) {
	if res == nil {
		return
	}
	fmt.Fprintf(w, "[model] %s (%s)\n", res.Response.Model, res.Response.Provider)                                                         //nolint:errcheck
	fmt.Fprintf(w, "[finish] %s\n", res.FinishReason.Reason)                                                                               //nolint:errcheck
	fmt.Fprintf(w, "[usage] in=%d out=%d total=%d\n", res.TotalUsage.InputTokens, res.TotalUsage.OutputTokens, res.TotalUsage.TotalTokens) //nolint:errcheck
}
