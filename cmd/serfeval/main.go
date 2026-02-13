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
	}
	flag.Parse()

	if *provider == "" || *model == "" || *task == "" {
		flag.Usage()
		os.Exit(1)
	}

	if err := runEval(*provider, *model, *strategy, *task, *workDir, *output); err != nil {
		fmt.Fprintf(os.Stderr, "serfeval: %v\n", err)
		os.Exit(1)
	}
}

func runEval(provider, model, strategy, task, workDir, output string) error {
	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client setup: %w", err)
	}

	profile, err := selectProfile(provider, model)
	if err != nil {
		return err
	}

	env := agent.NewLocalExecutionEnvironment(workDir)

	cfg := agent.SessionConfig{
		ContextStrategy: strategy,
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

	sess.Close()
	<-eventsDone

	metrics := collector.Metrics()
	metrics.DurationSeconds = elapsed.Seconds()
	metrics.Completed = err == nil
	metrics.Result = result

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

// selectProfile creates the ProviderProfile for the given provider and model.
// Mirrors cmd/serf/run.go selectProfile.
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
