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
// Answer every question: a real eval run once saw the model reply to only
// the first of two pending questions, so the instruction asks for one entry
// in answers per numbered question, in order; runRespond checks the count.
const respondSystemPromptTemplate = `You are playing a specific person who a coding agent is asking questions. Answer only using the facts in the brief below, speaking in the person's own voice. Keep answers short -- a sentence or two per question.

The user turn below lists every question, numbered. Answer every question: put one answer per question in the answers list, in the same order as the numbers, so the list has exactly as many entries as there are questions. Never skip a question or answer only the first one. If the brief does not cover what a question asks, that question's answer is "I don't know."

Brief:
%s`

// askExchange is one logged question/answer pair (--log's JSON-lines
// shape), read back by the harness into probeResult.Asks (readAskLog).
// Answer is the person's answer to that question alone: the model returns
// one answer per question (respondAnswerSchema).
type askExchange struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// runRespond implements the "respond" subcommand: it reads the pending
// ask_user questions as JSON from stdin (the same shape evener run's
// --ask-responder writes: {"questions": [agent.AskUserQuestion, ...]}),
// answers them with one model call playing the person described by
// --brief-file, prints the answers numbered, and — when --log is given — appends one
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
	answers, err := respondModelCall(context.Background(), *model, system, user)
	if err != nil {
		return fmt.Errorf("respond: %w", err)
	}
	if len(answers) != len(payload.Questions) {
		return fmt.Errorf("respond: model returned %d answers for %d questions", len(answers), len(payload.Questions))
	}
	var reply strings.Builder
	for i, a := range answers {
		a = strings.TrimSpace(a)
		if a == "" {
			return fmt.Errorf("respond: model returned an empty answer to question %d", i+1)
		}
		answers[i] = a
		if i > 0 {
			reply.WriteByte('\n')
		}
		fmt.Fprintf(&reply, "%d. %s", i+1, a)
	}

	if strings.TrimSpace(*logFile) != "" {
		if err := appendAskLog(*logFile, payload.Questions, answers); err != nil {
			return fmt.Errorf("respond: log question/answer: %w", err)
		}
	}
	fmt.Println(reply.String())
	return nil
}

// renderQuestionsForRespond renders the pending questions as plain text for
// the model's user turn, numbered "1.", "2.", ... unambiguously — the order
// the system prompt tells the model to follow in its answers list, one
// answer per question, so a multi-question round can't collapse into an
// answer for only the first one.
func renderQuestionsForRespond(questions []agent.AskUserQuestion) string {
	var b strings.Builder
	for i, q := range questions {
		if q.Header != "" {
			fmt.Fprintf(&b, "%d. (%s) %s\n", i+1, q.Header, q.Question)
		} else {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q.Question)
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
// with that question's own answer (answers[i]; runRespond has checked the
// counts match).
func appendAskLog(path string, questions []agent.AskUserQuestion, answers []string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	for i, q := range questions {
		line, err := json.Marshal(askExchange{Question: q.Question, Answer: answers[i]})
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
// an error — the caller gets an empty slice. malformed counts lines that did
// not parse, so the caller can report them instead of losing them silently.
func readAskLog(path string) (exchanges []askExchange, malformed int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var exchange askExchange
		if err := json.Unmarshal([]byte(line), &exchange); err != nil {
			malformed++
			continue
		}
		exchanges = append(exchanges, exchange)
	}
	return exchanges, malformed
}

// respondAnswerSchema is the JSON schema the model's answer must conform to
// — an "answers" array with one string per question, in question order, so
// parsing never depends on prose shape and a skipped question shows as a
// short list.
func respondAnswerSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"answers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"answers"},
	}
}

// respondModelCall is the model call respond makes to answer as the person.
// Production wires it to callRespondModel; tests replace runnerLoadClient
// instead (the LLM-boundary seam AGENTS.md's testing-boundary rule calls
// for), so this var exists for callers that need to fake the call itself.
var respondModelCall = callRespondModel

// callRespondModel resolves modelRef the same way the rest of this binary
// does (splitModelRef) and asks it, through the harness's own client
// loader (runnerLoadClient — never a key on the command line), for a JSON
// {"answers": [...]} object with one entry per question. The client carries
// every configured provider; GenerateObject picks modelRef's per request.
func callRespondModel(ctx context.Context, modelRef, systemPrompt, userPrompt string) ([]string, error) {
	providerName, modelName, err := splitModelRef(modelRef)
	if err != nil {
		return nil, err
	}
	client, err := runnerLoadClient("")
	if err != nil {
		return nil, fmt.Errorf("LLM client setup: %w", err)
	}
	system := systemPrompt
	user := userPrompt
	res, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
		Client:   client,
		Provider: providerName,
		Model:    modelName,
		System:   &system,
		Prompt:   &user,
		Schema:   respondAnswerSchema(),
	})
	if err != nil {
		return nil, fmt.Errorf("model call: %w", err)
	}
	// The schema validated the shape: answers is an array of strings.
	obj, _ := res.Output.(map[string]any)
	raw, _ := obj["answers"].([]any)
	answers := make([]string, len(raw))
	for i, a := range raw {
		answers[i], _ = a.(string)
	}
	return answers, nil
}
