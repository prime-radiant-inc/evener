package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func defCompact() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "compact",
		Description: "Compact your own context at a clean stopping point — between tasks, " +
			"after extracting results from a large context, before consuming substantial new " +
			"input, or before a complex multi-step operation. Your note_to_self is handed back " +
			"to you verbatim right after the compaction — a message from your pre-compaction " +
			"self — then cleared; pass an empty note_to_self to clear a pending note. " +
			"compaction_instructions (optional) steer what the summary keeps vs. drops. " +
			"In sessions without persistence, dropped detail is NOT recoverable, so be " +
			"conservative about what you instruct to drop.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note_to_self": map[string]any{
					"type":        "string",
					"description": "Note handed back to you verbatim right after the compaction, then cleared. Empty string clears a pending note.",
				},
				"compaction_instructions": map[string]any{
					"type":        "string",
					"description": "Optional: what the summary should preserve vs. drop.",
				},
			},
			"required": []string{"note_to_self"},
		},
	}
}

func registerCompactTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: defCompact()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			note, _ := args["note_to_self"].(string)
			instructions, _ := args["compaction_instructions"].(string)

			// Clearing a note (empty note, no instructions) does not force a compaction.
			if note == "" && instructions == "" {
				deps.setPinnedNote("")
				return tool.StateResult{Output: "Note cleared. No compaction requested."}, nil
			}
			if err := deps.requestForceCompact(instructions); err != nil {
				// Reject without mutating state — a rejected double-call must not
				// silently clobber the note set by the accepted first call.
				return nil, fmt.Errorf("compact: %w", err)
			}
			deps.setPinnedNote(note)
			// Compaction runs at the round tail, AFTER this returns — the message is a
			// prediction from current pressure, never past-tense.
			return tool.StateResult{Output: predictionMessage(note == "", deps.pressure())}, nil
		},
	})
}

// predictionMessage describes what the compact tool just scheduled. The compaction
// runs later at the round tail, so the wording is a prediction, never past-tense.
// pressure < lowPressurePredict means the history is light enough that the
// checkpoint/summary may condense little — but a compaction is still scheduled.
const lowPressurePredict = 0.30 // below this, accumulated history is small; condensation is likely minor

func predictionMessage(noteCleared bool, pressure float64) string {
	lead := "Note pinned."
	if noteCleared {
		lead = "Note cleared."
	}
	if pressure < lowPressurePredict {
		return lead + " Context is light, so the compaction will run but may condense little; your note will be handed back to you right after."
	}
	return lead + " A compaction will run at the seam, honoring your instructions; your note will be handed back to you right after."
}
