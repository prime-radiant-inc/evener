package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func defCompact() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "compact_context",
		Description: "Free up context-window headroom so you can keep working on a long task: " +
			"fold your own older conversation history into a compact summary checkpoint. This " +
			"reorganizes your working memory — it does NOT change your response style and does " +
			"NOT touch any files. Call it at a clean stopping point: between tasks, after " +
			"extracting what you need from bulky output, or before reading substantial new " +
			"input. Your note_to_self survives the compaction untouched and is handed back to " +
			"you verbatim immediately after — a message from your pre-compaction self — then " +
			"cleared. compaction_instructions (optional) steer what the summary keeps vs. drops. " +
			"In sessions without persistence, dropped detail is NOT recoverable, so be " +
			"conservative about what you instruct to drop.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note_to_self": map[string]any{
					"type": "string",
					"description": "Message to your post-compaction self, handed back verbatim right after " +
						"the compaction, then cleared. Put the exact strings a summary would mangle here: " +
						"IDs, paths, numbers, decisions, next steps. Empty string clears a pending note " +
						"(and, with no compaction_instructions, skips the compaction).",
				},
				"compaction_instructions": map[string]any{
					"type": "string",
					"description": "Optional steering for the summary: what to preserve in detail vs. drop. " +
						"Dropped detail is unrecoverable without persistence — steer toward keeping.",
				},
				"reload_skills": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "Optional selection of skills to reload from disk after the compaction: exact " +
						"canonical names from this session's loaded-skill inventory, in the order to reload " +
						"them. An explicit empty array reloads none. Omit (or pass null) to make no selection.",
				},
			},
			"required": []string{"note_to_self"},
		},
	}
}

func registerCompactTool(reg *tool.Registry, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Definition: defCompact(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			note, _ := args["note_to_self"].(string)
			instructions, _ := args["compaction_instructions"].(string)

			// Presence matters: re-encode the raw value so an absent key, an
			// explicit null, and an explicit empty array stay distinct.
			var selRaw json.RawMessage
			if v, ok := args["reload_skills"]; ok {
				raw, err := json.Marshal(v)
				if err != nil {
					return nil, fmt.Errorf("compact: reload_skills: %w", err)
				}
				selRaw = raw
			}
			selection := parseSkillReloadSelection(selRaw, deps.skillInventory())

			// The request is atomic and generation-owned: an empty note with
			// no instructions and an absent selection clears without
			// compaction; a present selection — even an explicit empty array —
			// requests one. A rejected double-call mutates nothing, and a
			// failed save reports a typed error instead of success.
			if _, err := deps.requestSkillCompaction(ctx, note, instructions, selection); err != nil {
				return nil, fmt.Errorf("compact: %w", err)
			}
			if note == "" && instructions == "" && selection.State == "absent" {
				return tool.StateResult{Output: "Note cleared. No compaction requested."}, nil
			}
			// Compaction runs at the round tail, AFTER this returns — the message is a
			// prediction from current pressure, never past-tense.
			out := predictionMessage(note == "", deps.pressure())
			if selection.State == "invalid" {
				out += " The reload_skills selection was invalid (" + selection.ErrorCode + ") and authorizes no reload."
			}
			return tool.StateResult{Output: out}, nil
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
