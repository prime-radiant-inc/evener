package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/llm"
)

// respondSystemPromptTemplate instructs the model to play the person the
// task's person: block describes, from the brief alone, in their own voice.
const respondSystemPromptTemplate = `You are playing a specific person who a coding agent is asking questions. Answer only using the facts in the brief below, speaking in the person's own voice. Keep answers short -- a sentence or two per question. If the brief does not cover what is being asked, say "I don't know."

Brief:
%s`

// askExchange is one logged question/answer pair (--log's JSON-lines
// shape), read back by the harness into probeResult.Asks (readAskLog).
type askExchange struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// runRespond implements the "respond" subcommand: it reads the pending
// ask_user questions as JSON from stdin (the same shape evener run's
// --ask-responder writes: {"questions": [agent.AskUserQuestion, ...]}),
// answers them with one model call playing the person described by
// --brief-file, prints the answer, and — when --log is given — appends one
// JSON line per question, pairing it with the answer, so the harness can
// record every question/answer pair in the run's result.json.
func runRespond(args []string) error {
	fs := flag.NewFlagSet("respond", flag.ContinueOnError)
	briefFile := fs.String("brief-file", "", "path to the person's brief")
	model := fs.String("model", "", "provider/model that plays the person")
	logFile := fs.String("log", "", "path to append question/answer JSON lines (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*briefFile) == "" {
		return errors.New("respond: --brief-file is required")
	}
	if strings.TrimSpace(*model) == "" {
		return errors.New("respond: --model is required")
	}
	brief, err := os.ReadFile(*briefFile)
	if err != nil {
		return fmt.Errorf("respond: read brief file: %w", err)
	}
	stdinJSON, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("respond: read questions from stdin: %w", err)
	}
	var payload struct {
		Questions []agent.AskUserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(stdinJSON, &payload); err != nil {
		return fmt.Errorf("respond: parse questions JSON: %w", err)
	}
	if len(payload.Questions) == 0 {
		return errors.New("respond: no questions on stdin")
	}

	system := fmt.Sprintf(respondSystemPromptTemplate, strings.TrimSpace(string(brief)))
	user := renderQuestionsForRespond(payload.Questions)
	answer, err := respondModelCall(context.Background(), *model, system, user)
	if err != nil {
		return fmt.Errorf("respond: %w", err)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return errors.New("respond: model returned an empty answer")
	}

	if strings.TrimSpace(*logFile) != "" {
		if err := appendAskLog(*logFile, payload.Questions, answer); err != nil {
			return fmt.Errorf("respond: log question/answer: %w", err)
		}
	}
	fmt.Println(answer)
	return nil
}

// renderQuestionsForRespond renders the pending questions as plain text for
// the model's user turn, options and details included.
func renderQuestionsForRespond(questions []agent.AskUserQuestion) string {
	var b strings.Builder
	for i, q := range questions {
		if q.Header != "" {
			fmt.Fprintf(&b, "Question %d (%s): %s\n", i+1, q.Header, q.Question)
		} else {
			fmt.Fprintf(&b, "Question %d: %s\n", i+1, q.Question)
		}
		for _, o := range q.Options {
			if o.Detail != "" {
				fmt.Fprintf(&b, "  - %s: %s\n", o.Label, o.Detail)
			} else {
				fmt.Fprintf(&b, "  - %s\n", o.Label)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// appendAskLog appends one JSON line per question in questions, each paired
// with the same answer (respond makes one model call per invocation,
// covering every question that round posted).
func appendAskLog(path string, questions []agent.AskUserQuestion, answer string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	for _, q := range questions {
		line, err := json.Marshal(askExchange{Question: q.Question, Answer: answer})
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// readAskLog reads back the question/answer pairs respond appended to a
// --log file, for the harness to attach to a probe's result. A missing or
// unreadable log (no person: block, or a responder that never ran) is not
// an error — the caller gets an empty slice.
func readAskLog(path string) []askExchange {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []askExchange
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var exchange askExchange
		if err := json.Unmarshal([]byte(line), &exchange); err != nil {
			continue
		}
		out = append(out, exchange)
	}
	return out
}

// respondAnswerSchema is the JSON schema the model's answer must conform to
// — a single "answer" string, so parsing never depends on prose shape.
func respondAnswerSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"required": []string{"answer"},
	}
}

// respondModelCall is the model call respond makes to answer as the person.
// Production wires it to callRespondModel; tests replace runnerLoadClient
// instead (the LLM-boundary seam AGENTS.md's testing-boundary rule calls
// for), so this var exists for callers that need to fake the call itself.
var respondModelCall = callRespondModel

// callRespondModel resolves modelRef the same way the rest of this binary
// does (splitModelRef) and asks it, through the harness's own client
// loader (runnerLoadClient — never a key on the command line), for a single
// JSON {"answer": "..."} object.
func callRespondModel(ctx context.Context, modelRef, systemPrompt, userPrompt string) (string, error) {
	providerName, modelName, err := splitModelRef(modelRef)
	if err != nil {
		return "", err
	}
	client, err := runnerLoadClient("")
	if err != nil {
		return "", fmt.Errorf("LLM client setup: %w", err)
	}
	system := systemPrompt
	user := userPrompt
	res, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
		GenerateOptions: llm.GenerateOptions{
			Client:   client,
			Provider: providerName,
			Model:    modelName,
			System:   &system,
			Prompt:   &user,
		},
		Schema: respondAnswerSchema(),
	})
	if err != nil {
		return "", fmt.Errorf("model call: %w", err)
	}
	obj, _ := res.Output.(map[string]any)
	answer, _ := obj["answer"].(string)
	return answer, nil
}
