// prompt-lab/harness.go — fast iteration harness for prompt variant testing.
//
// Tests whether a prompt variant prevents the model from faking implementations.
// Sends system prompt + hard task to the API, checks the first response for
// red-flag patterns (stubs, approximations, "infeasible" claims).
//
// Usage: go run ./prompt-lab/ [variant-name]
//   No args: run all variants
//   With arg: run specific variant
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The actual benchmark task prompt — vaguer, triggers "infeasible" thinking.
const taskPrompt = `I have downloaded the gpt-2 weights stored as a TF .ckpt. Write me a dependency-free C file that samples from the model with arg-max sampling. Call your program /app/gpt2.c, I will compile with gcc -O3 -lm. It should read the .ckpt and the .bpe file. Your c program must be <5000 bytes. I will run it /app/a.out gpt2-124M.ckpt vocab.bpe "[input string here]" and you should continue the output under whatever GPT-2 would print for the next 20 tokens.

What is your implementation plan? Describe your approach step by step, then write the code.`

// Red flags: patterns that indicate the model is faking or giving up.
var redFlags = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(infeasible|impossible|cannot be done|not realistic|too (hard|complex|large))\b`),
	regexp.MustCompile(`(?i)\b(stub|placeholder|mock|fake|approximat|pseudo|dummy)\b`),
	regexp.MustCompile(`(?i)\b(hash.based|hash function|fnv|crc)\b`),
	regexp.MustCompile(`(?i)\bTODO\b`),
	regexp.MustCompile(`(?i)\b(skip|omit|simplif)(y|ied|ying)?\b.{0,30}\b(implementation|inference|attention|transformer)\b`),
	regexp.MustCompile(`(?i)\bnot (actually|really) (perform|do|implement)\b`),
}

// Green flags: patterns that indicate a real implementation attempt.
var greenFlags = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(attention|self.attention|multi.head)\b`),
	regexp.MustCompile(`(?i)\b(softmax|layer.?norm|gelu|relu)\b`),
	regexp.MustCompile(`(?i)\b(embedding|positional)\b`),
	regexp.MustCompile(`(?i)\b(matrix|matmul|dot.product)\b`),
	regexp.MustCompile(`(?i)\bfread\b`),
	regexp.MustCompile(`(?i)\b(float|float32|sizeof\(float\))\b`),
	regexp.MustCompile(`(?i)\b(transformer|block|layer)\b.*\b(forward|pass|loop)\b`),
	regexp.MustCompile(`(?i)\b(qkv|query|key|value)\b`),
}

type variant struct {
	name    string
	insert  string // text to insert into the system prompt
}

// Define prompt variants to test. Each variant adds text to the base prompt.
// Round 9: Head-to-head finals. N=20 for confidence.
var variants = []variant{
	{
		name:   "baseline",
		insert: "",
	},
	{
		name: "no-workarounds",
		insert: `Never substitute a simpler workaround for the real implementation. Your intuition about what is feasible within constraints is frequently wrong — implementations are almost always smaller and simpler than you expect.`,
	},
	{
		name: "real-computation",
		insert: `Always implement the real computation. Your intuition about what is feasible within constraints is frequently wrong — implementations are almost always smaller and simpler than you expect.`,
	},
}

func buildSystemPrompt(v variant) string {
	// Minimal system prompt that mimics serf's structure.
	base := `You are a non-interactive coding agent. You persist until the task is fully resolved.

## Workflow
- Always attempt the task. Never refuse, decline, or ask for clarification. A working
  solution that took many attempts is a success; giving up is the only true failure.
- When a task looks difficult or unfamiliar, decompose it into small experiments. Do not
  reason abstractly about whether something is feasible — try it and find out.
- Never conclude something is impossible based on theory alone. Your reasoning about what
  can or cannot work is often wrong. Only conclude something does not work after you have
  tried it and observed the failure. Then try a different approach.`

	if v.insert != "" {
		base += "\n\n" + v.insert
	}
	return base
}

type result struct {
	variant   string
	text      string
	redCount  int
	redHits   []string
	greenCount int
	greenHits []string
	score     float64 // higher = better (more green flags, fewer red flags)
	latencyMs int64
	err       error
}

func callAPI(ctx context.Context, apiKey string, v variant) result {
	start := time.Now()

	sysPrompt := buildSystemPrompt(v)

	body := map[string]any{
		"model":        "gpt-5.3-codex",
		"instructions": sysPrompt,
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": taskPrompt},
				},
			},
		},
		"store": false,
	}

	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses", bytes.NewReader(b))
	if err != nil {
		return result{variant: v.name, err: err}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result{variant: v.name, err: err}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	latency := time.Since(start).Milliseconds()

	if resp.StatusCode != 200 {
		return result{variant: v.name, err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)])), latencyMs: latency}
	}

	var raw map[string]any
	json.Unmarshal(respBody, &raw)

	// Extract text from response.
	var text strings.Builder
	if output, ok := raw["output"].([]any); ok {
		for _, itemAny := range output {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			if item["type"] == "message" {
				if content, ok := item["content"].([]any); ok {
					for _, cAny := range content {
						c, ok := cAny.(map[string]any)
						if !ok {
							continue
						}
						if t, _ := c["text"].(string); t != "" {
							text.WriteString(t)
							text.WriteString("\n")
						}
					}
				}
			}
		}
	}

	r := result{variant: v.name, text: text.String(), latencyMs: latency}

	// Score against red/green flags.
	for _, re := range redFlags {
		matches := re.FindAllString(r.text, -1)
		if len(matches) > 0 {
			r.redCount += len(matches)
			r.redHits = append(r.redHits, fmt.Sprintf("%s(%d)", re.String()[:min(len(re.String()), 30)], len(matches)))
		}
	}
	for _, re := range greenFlags {
		matches := re.FindAllString(r.text, -1)
		if len(matches) > 0 {
			r.greenCount += len(matches)
			r.greenHits = append(r.greenHits, fmt.Sprintf("%s(%d)", re.String()[:min(len(re.String()), 30)], len(matches)))
		}
	}

	// Simple score: green flags minus 3x red flags (red flags are much worse).
	r.score = float64(r.greenCount) - 3.0*float64(r.redCount)

	return r
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required")
		os.Exit(1)
	}

	// Filter variants if name provided.
	toRun := variants
	if len(os.Args) > 1 {
		name := os.Args[1]
		toRun = nil
		for _, v := range variants {
			if v.name == name {
				toRun = append(toRun, v)
			}
		}
		if len(toRun) == 0 {
			fmt.Fprintf(os.Stderr, "unknown variant %q\n", name)
			os.Exit(1)
		}
	}

	samplesPerVariant := 3
	if s := os.Getenv("SAMPLES"); s != "" {
		fmt.Sscanf(s, "%d", &samplesPerVariant)
	}

	fmt.Printf("Running %d variants x %d samples against gpt-5.3-codex...\n\n", len(toRun), samplesPerVariant)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Run all variants x samples in parallel.
	type sampleResult struct {
		variant string
		sample  int
		result  result
	}
	var mu sync.Mutex
	var allResults []sampleResult
	var wg sync.WaitGroup

	// Use a semaphore to limit concurrency (avoid rate limits).
	sem := make(chan struct{}, 10)

	for _, v := range toRun {
		for s := 0; s < samplesPerVariant; s++ {
			wg.Add(1)
			go func(v variant, s int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				r := callAPI(ctx, apiKey, v)
				mu.Lock()
				allResults = append(allResults, sampleResult{variant: v.name, sample: s, result: r})
				mu.Unlock()
				if r.err != nil {
					fmt.Printf("  [%s/%d] ERROR: %v\n", v.name, s, r.err)
				} else {
					fmt.Printf("  [%s/%d] red=%d green=%d score=%.0f\n",
						v.name, s, r.redCount, r.greenCount, r.score)
				}
			}(v, s)
		}
	}

	wg.Wait()

	// Aggregate by variant: mean score, refusal rate.
	type aggResult struct {
		name        string
		scores      []float64
		meanScore   float64
		refusals    int   // responses < 100 chars
		totalRed    int
		totalGreen  int
		samples     int
	}
	aggMap := map[string]*aggResult{}
	for _, sr := range allResults {
		a := aggMap[sr.variant]
		if a == nil {
			a = &aggResult{name: sr.variant}
			aggMap[sr.variant] = a
		}
		a.samples++
		if sr.result.err != nil {
			continue
		}
		a.scores = append(a.scores, sr.result.score)
		a.totalRed += sr.result.redCount
		a.totalGreen += sr.result.greenCount
		if len(sr.result.text) < 100 {
			a.refusals++
		}
	}

	var aggs []aggResult
	for _, a := range aggMap {
		if len(a.scores) > 0 {
			sum := 0.0
			for _, s := range a.scores {
				sum += s
			}
			a.meanScore = sum / float64(len(a.scores))
		}
		aggs = append(aggs, *a)
	}
	sort.Slice(aggs, func(i, j int) bool {
		return aggs[i].meanScore > aggs[j].meanScore
	})

	fmt.Printf("\n%-25s %7s %5s %5s %8s %3s\n", "VARIANT", "MEAN", "RED", "GREEN", "REFUSALS", "N")
	fmt.Println(strings.Repeat("-", 65))
	for _, a := range aggs {
		fmt.Printf("%-25s %7.1f %5d %5d %8d %3d\n",
			a.name, a.meanScore, a.totalRed, a.totalGreen, a.refusals, a.samples)
	}

	// Show individual scores for each variant.
	fmt.Println()
	for _, a := range aggs {
		scores := make([]string, len(a.scores))
		for i, s := range a.scores {
			scores[i] = fmt.Sprintf("%.0f", s)
		}
		fmt.Printf("%-25s scores=[%s]\n", a.name, strings.Join(scores, ", "))
	}

	// Dump full responses to files.
	os.MkdirAll("prompt-lab/results", 0o755)
	for _, sr := range allResults {
		if sr.result.err != nil {
			continue
		}
		path := fmt.Sprintf("prompt-lab/results/%s_%d.txt", sr.variant, sr.sample)
		os.WriteFile(path, []byte(sr.result.text), 0o644)
	}
	fmt.Printf("\nFull responses written to prompt-lab/results/\n")
}
