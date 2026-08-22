// Command extract-session reads a evener transcript JSONL and emits a compact
// JSON projection suitable for embedding in visualization mockups: per-turn
// kind, timestamp, usage, tool-call names/errors, and short text previews for
// user/steering/assistant turns. It decodes with the canonical
// agent/transcript + agent/schema types — no guessed JSON shapes.
//
// Usage: go run ./proposals/transcript-viz/_extract <transcript.jsonl> <out.json>
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

const (
	userTextCap     = 400 // user/steering prompts need enough text to be recognizable anchors
	assistantCap    = 200
	toolArgCap      = 120
	toolResultKBcap = 0 // results are summarized by byte size only
)

type ToolCallOut struct {
	Name   string `json:"n"`
	Target string `json:"t,omitempty"` // file path / command / first arg, capped
	Err    bool   `json:"e,omitempty"`
}

type TurnOut struct {
	I         int          `json:"i"`
	Kind      string       `json:"kind"`
	TS        time.Time    `json:"ts"`
	InTok     int          `json:"in,omitempty"`
	OutTok    int          `json:"out,omitempty"`
	Text      string       `json:"text,omitempty"`
	Calls     []ToolCallOut `json:"calls,omitempty"`
	ResultB   int          `json:"res_bytes,omitempty"`
	SteerSrc  string       `json:"steer_src,omitempty"`
}

type SessionOut struct {
	ID       string    `json:"id"`
	Model    string    `json:"model"`
	Parent   string    `json:"parent,omitempty"`
	Depth    int       `json:"depth"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	Turns    int       `json:"turns"`
	Stream   []TurnOut `json:"stream"`
}

func capText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// callTarget extracts a short human-meaningful target from common tool args.
func callTarget(name string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "command", "pattern", "url", "skill_name", "task"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return capText(s, toolArgCap)
			}
		}
	}
	return ""
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: extract-session <transcript.jsonl> <out.json>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	var out SessionOut
	// Pair tool errors by call ID: results live on later TOOL_RESULTS turns.
	callIndex := map[string]int{} // callID -> index into last assistant turn's Calls
	var lastCallTurn = -1

	dec := json.NewDecoder(bufio.NewReaderSize(f, 4<<20))
	for dec.More() {
		var line json.RawMessage
		if err := dec.Decode(&line); err != nil {
			fmt.Fprintln(os.Stderr, "decode:", err)
			os.Exit(1)
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		switch probe.Kind {
		case "header":
			var h transcript.Header
			if json.Unmarshal(line, &h) == nil {
				out.ID = h.SessionID
				out.Model = h.Model
				out.Parent = h.ParentSessionID
				out.Depth = h.Depth
			}
		case "entry":
			var e transcript.Entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			t := e.Turn
			to := TurnOut{I: len(out.Stream), Kind: string(t.Kind), TS: t.Timestamp, SteerSrc: t.SteeringSource}
			if t.Usage.TotalTokens > 0 {
				to.InTok, to.OutTok = t.Usage.InputTokens, t.Usage.OutputTokens
			}
			for _, p := range t.Message.Content {
				switch p.Kind {
				case llm.ContentText:
					switch t.Kind {
					case "USER_INPUT", "STEERING":
						to.Text = capText(to.Text+p.Text, userTextCap)
					case "ASSISTANT":
						to.Text = capText(to.Text+p.Text, assistantCap)
					case "CHECKPOINT", "SUMMARY", "SYSTEM":
						to.Text = capText(to.Text+p.Text, assistantCap)
					}
				case llm.ContentToolCall:
					if p.ToolCall != nil {
						to.Calls = append(to.Calls, ToolCallOut{
							Name:   p.ToolCall.Name,
							Target: callTarget(p.ToolCall.Name, p.ToolCall.Arguments),
						})
						callIndex[p.ToolCall.ID] = len(to.Calls) - 1
						lastCallTurn = to.I
					}
				case llm.ContentToolResult:
					if p.ToolResult != nil {
						to.ResultB += len(fmt.Sprint(p.ToolResult.Content))
						if p.ToolResult.IsError && lastCallTurn >= 0 {
							if ci, ok := callIndex[p.ToolResult.ToolCallID]; ok {
								out.Stream[lastCallTurn].Calls[ci].Err = true
							}
						}
					}
				}
			}
			out.Stream = append(out.Stream, to)
		}
	}
	out.Turns = len(out.Stream)
	if out.Turns > 0 {
		out.Started = out.Stream[0].TS
		out.Ended = out.Stream[out.Turns-1].TS
	}
	enc := json.NewEncoder(os.Stdout)
	if len(os.Args) == 3 && os.Args[2] != "-" {
		of, err := os.Create(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer of.Close()
		enc = json.NewEncoder(of)
	}
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}
