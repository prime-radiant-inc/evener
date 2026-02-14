// serfeval runs a serf task with a given context strategy and outputs
// structured evaluation metrics as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/openai"
)

func main() {
	provider := flag.String("provider", "", "LLM provider (openai, anthropic, google)")
	model := flag.String("model", "", "LLM model identifier")
	strategy := flag.String("strategy", "compact", "context strategy: compact|recall|session-log|ooda")
	task := flag.String("task", "", "task description")
	workDir := flag.String("dir", ".", "working directory")
	output := flag.String("output", "", "output JSON file (default: stdout)")
	probes := flag.String("probes", "", "path to JSON file with retention probe questions")
	thresholdScale := flag.Float64("threshold-scale", 0, "multiply compaction thresholds (e.g., 0.1 = trigger at 10% of normal)")
	maxTurns := flag.Int("max-turns", 0, "maximum agent turns before stopping (0 = unlimited)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serfeval --provider <p> --model <m> --task <task> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Run a serf task and output evaluation metrics as JSON.\n\n")
		fmt.Fprintf(os.Stderr, "Required:\n")
		fmt.Fprintf(os.Stderr, "  --provider <name>    LLM provider: openai, anthropic, google\n")
		fmt.Fprintf(os.Stderr, "  --model <name>       LLM model\n")
		fmt.Fprintf(os.Stderr, "  --task <text>        Task description\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  --strategy <name>    Context strategy (default: compact)\n")
		fmt.Fprintf(os.Stderr, "  --dir <path>         Working directory (default: .)\n")
		fmt.Fprintf(os.Stderr, "  --output <path>      Write JSON to file instead of stdout\n")
		fmt.Fprintf(os.Stderr, "  --probes <path>      JSON file with retention probe questions\n")
		fmt.Fprintf(os.Stderr, "  --threshold-scale <f> Multiply compaction thresholds (e.g., 0.1)\n")
		fmt.Fprintf(os.Stderr, "  --max-turns <n>      Maximum agent turns (0 = unlimited)\n")
	}
	flag.Parse()

	if *provider == "" || *model == "" || *task == "" {
		flag.Usage()
		os.Exit(1)
	}

	if err := runEval(*provider, *model, *strategy, *task, *workDir, *output, *probes, *thresholdScale, *maxTurns); err != nil {
		fmt.Fprintf(os.Stderr, "serfeval: %v\n", err)
		os.Exit(1)
	}
}

func runEval(provider, model, strategy, task, workDir, output, probesFile string, thresholdScale float64, maxTurns int) error {
	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	profile, err := selectProfile(provider, model)
	if err != nil {
		return err
	}

	env := agent.NewLocalExecutionEnvironment(workDir)

	stateDir, err := os.MkdirTemp("", "serfeval-state-")
	if err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	defer os.RemoveAll(stateDir)

	cfg := agent.SessionConfig{
		ContextStrategy:          strategy,
		CompactionThresholdScale: thresholdScale,
		StateDir:                 stateDir,
		MaxTurns:                 maxTurns,
	}

	sess, err := agent.NewSession(client, profile, env, cfg)
	if err != nil {
		return fmt.Errorf("session creation: %w", err)
	}
	defer sess.Close()

	collector := agent.NewEvalCollector(strategy, model, task)

	// Drain events in background.
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			collector.ProcessEvent(ev)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	start := time.Now()
	result, err := sess.ProcessInput(ctx, task)
	elapsed := time.Since(start)

	// Grab session history before closing for retention probes.
	snap := sess.Snapshot()

	sess.Close()
	<-eventsDone

	metrics := collector.Metrics()
	metrics.DurationSeconds = elapsed.Seconds()
	metrics.Completed = err == nil
	metrics.Result = result

	// Run retention probes if a probes file was provided.
	if probesFile != "" {
		probeQuestions, loadErr := loadProbeQuestions(probesFile)
		if loadErr != nil {
			return fmt.Errorf("load probes: %w", loadErr)
		}
		// Use a fresh context for probes -- the task context may have been cancelled.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer probeCancel()
		retScore, probeErr := agent.RunRetentionProbes(probeCtx, client, profile, probeQuestions, snap.History)
		if probeErr != nil {
			return fmt.Errorf("retention probes: %w", probeErr)
		}
		metrics.RetentionScore = retScore
	}

	data, marshalErr := json.MarshalIndent(metrics, "", "  ")
	if marshalErr != nil {
		return fmt.Errorf("marshal metrics: %w", marshalErr)
	}

	if output != "" {
		return os.WriteFile(output, append(data, '\n'), 0o644)
	}
	fmt.Println(string(data))
	return nil
}

// loadProbeQuestions reads a JSON file containing an array of probe question strings.
func loadProbeQuestions(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var questions []string
	if err := json.Unmarshal(data, &questions); err != nil {
		return nil, fmt.Errorf("parse probes JSON: %w", err)
	}
	return questions, nil
}

// selectProfile creates a ProviderProfile for the given provider name.
// NOTE: Simplified version of cmd/serf/run.go:selectProfile. Keep in sync.
func selectProfile(provider, model string) (agent.ProviderProfile, error) {
	switch strings.ToLower(provider) {
	case "openai":
		return agent.NewOpenAIProfile(model), nil
	case "anthropic":
		return agent.NewAnthropicProfile(model), nil
	case "google", "gemini":
		return agent.NewGeminiProfile(model), nil
	default:
		return nil, fmt.Errorf("unknown provider %q: must be openai, anthropic, or google", provider)
	}
}
