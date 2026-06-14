package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// MemoryCrystal is a structured micro-summary of key facts from a session action.
type MemoryCrystal struct {
	Turn   int    `json:"turn"`   // turn index the crystal was extracted from
	Action string `json:"action"` // the action that produced the facts
	Facts  string `json:"facts"`  // compact, machine-readable key facts
}

// MemoryCrystalsStrategy uses compact compaction as the base, but periodically
// crystallizes key facts into tiny structured summaries that survive all
// compaction. Unlike session-log prose, crystals are machine-readable, small
// (<100 tokens each), and cumulative.
//
// AfterAction: every 3rd action, call cheap model to extract key facts.
// ManageContext: run compact, then inject crystal bank as steering message.
type MemoryCrystalsStrategy struct {
	cm       *Manager
	crystals []MemoryCrystal
}

// NewMemoryCrystalsStrategy returns a MemoryCrystalsStrategy that uses the
// given Manager and starts with an empty crystal bank.
func NewMemoryCrystalsStrategy(cm *Manager) *MemoryCrystalsStrategy {
	return &MemoryCrystalsStrategy{
		cm:       cm,
		crystals: []MemoryCrystal{},
	}
}

// Name returns the strategy's identifier, "memory-crystals".
func (s *MemoryCrystalsStrategy) Name() string { return "memory-crystals" }

// Tools returns the tools registered by this strategy; it registers none.
func (s *MemoryCrystalsStrategy) Tools() []tool.RegisteredTool { return nil }

// ManageContext runs standard compact compaction and then, if any crystals
// have been collected, injects the crystal bank into history as a steering
// message.
func (s *MemoryCrystalsStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	// Run standard compact compaction.
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)

	// After compaction, inject crystal bank if we have any.
	if len(s.crystals) > 0 {
		s.injectCrystals(history)
	}

	return nil
}

// injectCrystals ensures the crystal bank is present in history as a steering
// message at the end. Removes any previous crystal turn first.
func (s *MemoryCrystalsStrategy) injectCrystals(history *[]schema.Turn) {
	var b strings.Builder
	b.WriteString("[MEMORY CRYSTALS]\nKey facts preserved from this session:\n\n")
	for _, c := range s.crystals {
		b.WriteString(fmt.Sprintf("Turn %d [%s]: %s\n", c.Turn, c.Action, c.Facts))
	}
	b.WriteString("[END CRYSTALS]")

	// Remove any existing crystal turn.
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), "[MEMORY CRYSTALS]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append crystal turn at the end (within preserved window for next compaction).
	crystalTurn := schema.NewTurn(schema.TurnSteering, llm.User(b.String()))
	*history = append(*history, crystalTurn)
}

// AfterAction crystallizes key facts from the recent action every 3rd turn.
// Calling every turn would trigger the compaction cascade; every 3rd is a
// good balance between coverage and overhead.
func (s *MemoryCrystalsStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	if client == nil || s.cm == nil {
		return nil
	}

	turnCount := len(history)
	// Only crystallize every 3rd action to reduce overhead.
	if turnCount%3 != 0 {
		return nil
	}

	// Get last few turns for context.
	recent := history
	if len(recent) > 6 {
		recent = recent[len(recent)-6:]
	}

	crystal, err := s.crystallize(ctx, client, recent, turnCount)
	if err != nil {
		return nil //nolint:nilerr // crystallization is a best-effort optimization; failure is non-fatal
	}

	s.crystals = append(s.crystals, crystal)
	s.pruneOldCrystals()

	return nil
}

// crystallize calls the cheap model to extract key facts from recent turns.
func (s *MemoryCrystalsStrategy) crystallize(ctx context.Context, client *llm.Client, recent []schema.Turn, turnNumber int) (MemoryCrystal, error) {
	var b strings.Builder
	for _, t := range recent {
		switch t.Kind {
		case schema.TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 200) + "\n")
		case schema.TurnTool, schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					errTag := ""
					if p.ToolResult.IsError {
						errTag = " ERROR"
					}
					b.WriteString(fmt.Sprintf("Tool(%s)%s: %s\n", p.ToolResult.Name, errTag, truncate(content, 200)))
				}
			}
		}
	}

	prompt := fmt.Sprintf(`Extract the key facts from this coding agent action. Output a single line (under 100 tokens) listing ONLY concrete facts: file paths modified, values discovered, test results, decisions made, errors encountered. No prose.

Recent action:
%s
Key facts (one line):`, b.String())

	cp := s.cm.currentProfile()
	cheapProvider, cheapModel := cp.CheapModelRef()
	req := llm.Request{
		Model:    cheapModel,
		Provider: cheapProvider,
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return MemoryCrystal{}, err
	}

	// Determine action name from recent turns.
	action := "unknown"
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].Kind == schema.TurnTool || recent[i].Kind == schema.TurnToolResults {
			for _, p := range recent[i].Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					action = p.ToolResult.Name
					break
				}
			}
			break
		}
	}

	return MemoryCrystal{
		Turn:   turnNumber,
		Action: action,
		Facts:  strings.TrimSpace(resp.Text()),
	}, nil
}

// pruneOldCrystals caps the crystal bank to avoid unbounded growth.
// 20 crystals at ~100 tokens each = ~2k tokens total.
func (s *MemoryCrystalsStrategy) pruneOldCrystals() {
	const maxCrystals = 20
	if len(s.crystals) > maxCrystals {
		s.crystals = s.crystals[len(s.crystals)-maxCrystals:]
	}
}
