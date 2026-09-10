package apptranscript_test

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func TestProjectTurnRoundTimingsPreservesStructuredPayload(t *testing.T) {
	turn := schema.NewTurn(schema.TurnRoundTimings, llm.System("timing"))
	turn.RoundTimings = &schema.RoundTimings{Round: 2, LLMCall: 3 * time.Second, TotalRound: 4 * time.Second}
	items := apptranscript.ProjectTurn("turn_1", 7, turn, nil, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Type != "systemMessage" || item.EventKind != appwire.ThreadItemEventKindRoundTimings || item.Description != "Round timings" {
		t.Fatalf("item identity = %#v", item)
	}
	var raw struct {
		RoundTimings schema.RoundTimings `json:"roundTimings"`
	}
	if err := json.Unmarshal(item.Raw, &raw); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if raw.RoundTimings.Round != 2 || raw.RoundTimings.LLMCall != 3*time.Second {
		t.Fatalf("raw timing = %#v", raw.RoundTimings)
	}
}
