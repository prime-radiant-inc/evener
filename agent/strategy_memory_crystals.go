package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// MemoryCrystal is a structured micro-summary of key facts from a session action.
type MemoryCrystal struct {
	Turn   int    `json:"turn"`
	Action string `json:"action"`
	Facts  string `json:"facts"`
}

// memoryCrystalsStrategy uses compact compaction as the base, but periodically
// crystallizes key facts into tiny structured summaries that survive all
// compaction. Unlike session-log prose, crystals are machine-readable, small
// (<100 tokens each), and cumulative.
//
// AfterAction: every 3rd action, call cheap model to extract key facts.
// ManageContext: run compact, then inject crystal bank as steering message.
type memoryCrystalsStrategy struct {
	cm       *contextManager
	crystals []MemoryCrystal
}

// newMemoryCrystalsStrategy returns a memoryCrystalsStrategy that uses the
// given contextManager and starts with an empty crystal bank.
func newMemoryCrystalsStrategy(cm *contextManager) *memoryCrystalsStrategy {
	return &memoryCrystalsStrategy{
		cm:       cm,
		crystals: []MemoryCrystal{},
	}
}

// Name returns the strategy's identifier, "memory-crystals".
func (s *memoryCrystalsStrategy) Name() string { return "memory-crystals" }

// Tools returns the tools registered by this strategy; it registers none.
func (s *memoryCrystalsStrategy) Tools() []registeredTool { return nil }

// ManageContext runs standard compact compaction and then, if any crystals
// have been collected, injects the crystal bank into history as a steering
// message.
func (s *memoryCrystalsStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, EventData)) error {
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
func (s *memoryCrystalsStrategy) injectCrystals(history *[]Turn) {
	var b strings.Builder
	b.WriteString("[MEMORY CRYSTALS]\nKey facts preserved from this session:\n\n")
	for _, c := range s.crystals {
		b.WriteString(fmt.Sprintf("Turn %d [%s]: %s\n", c.Turn, c.Action, c.Facts))
	}
	b.WriteString("[END CRYSTALS]")

	// Remove any existing crystal turn.
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == TurnSteering && strings.Contains(t.Message.Text(), "[MEMORY CRYSTALS]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append crystal turn at the end (within preserved window for next compaction).
	crystalTurn := NewTurn(TurnSteering, llm.User(b.String()))
	*history = append(*history, crystalTurn)
}

// AfterAction crystallizes key facts from the recent action every 3rd turn.
// Calling every turn would trigger the compaction cascade; every 3rd is a
// good balance between coverage and overhead.
func (s *memoryCrystalsStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
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
		return nil // Non-fatal.
	}

	s.crystals = append(s.crystals, crystal)
	s.pruneOldCrystals()

	return nil
}

// crystallize calls the cheap model to extract key facts from recent turns.
func (s *memoryCrystalsStrategy) crystallize(ctx context.Context, client *llm.Client, recent []Turn, turnNumber int) (MemoryCrystal, error) {
	var b strings.Builder
	for _, t := range recent {
		switch t.Kind {
		case TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 200) + "\n")
		case TurnTool, TurnToolResults:
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
	req := llm.Request{
		Model:    cp.CheapModel(),
		Provider: cp.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return MemoryCrystal{}, err
	}

	// Determine action name from recent turns.
	action := "unknown"
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].Kind == TurnTool || recent[i].Kind == TurnToolResults {
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
func (s *memoryCrystalsStrategy) pruneOldCrystals() {
	const maxCrystals = 20
	if len(s.crystals) > maxCrystals {
		s.crystals = s.crystals[len(s.crystals)-maxCrystals:]
	}
}
