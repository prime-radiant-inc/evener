package agent

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const (
	// askUserUnavailableErr is the exec-time guard's error (spec §7 point 4):
	// config drift and future registration refactors both land here instead of
	// a panic or a silent no-op.
	askUserUnavailableErr = "ask_user unavailable: no interactive user in this session; decide with your best judgment"

	// askUserAckText is returned on a successful ask_user call (spec §5.1).
	askUserAckText = "questions posted; answers arrive in the user's reply after your turn ends"
)

// askQuestion is one question posted by an ask_user call, recorded in the
// session's per-turn pending set (spec §5.1) purely so a round-boundary check
// can tell whether the round just posted question(s). The transcript remains
// the durable, renderable record of the questions and their options (spec
// §5.1, §6); this struct carries just enough to identify a pending question,
// not the full option/detail payload.
type askQuestion struct {
	Header   string
	Question string
}

// isSubagentSession reports whether this session is a subagent, for the
// ask_user root-only gate (spec §7.1): a live spawn (cfg.spawn.parentSessionID,
// covering delegate spawns and job restores) OR a session restored from a
// persisted meta whose IsSubagent flag was set. The flag alone catches a bare
// `serve --resume <delegate-id>`: spawn is never persisted (json:"-"), so that
// restore path leaves cfg.spawn.parentSessionID empty. A forked root is NOT a
// subagent — fork lineage lives in meta.ParentSessionID with IsSubagent==false
// (Session.Meta already keeps the two concepts apart when writing the flag),
// a distinct concept from cfg.spawn.parentSessionID.
func (s *Session) isSubagentSession() bool {
	return s.cfg.spawn.parentSessionID != "" || s.restoredMetaIsSubagent
}

// askPendingCount returns the number of questions currently pending this turn
// (spec §5.1's per-turn pending set) — a later round-boundary check uses this
// to decide whether the round just posted question(s).
func (s *Session) askPendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.askPending)
}

// clearAskPending empties the pending set. Nothing in this task calls it in
// production: a later task wires the actual clear points (a resolving user
// turn, an interrupted turn).
func (s *Session) clearAskPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.askPending = nil
}

// registerAskTool registers ask_user. registerCoreTools calls this only when
// the session is interactive and root (spec §7 point 1); the exec-time guard
// below is defense in depth for config drift (spec §7 point 4).
func registerAskTool(reg *tool.Registry, s *Session, deps *toolDeps) {
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefAskUser()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			if s.cfg.NonInteractive || s.isSubagentSession() {
				return nil, errors.New(askUserUnavailableErr)
			}

			// Schema-level shape (question/option counts, header maxLength,
			// required fields) is already enforced by the registry's JSON-Schema
			// validation before Exec runs (spec §5.1) — only the two semantic
			// rules the schema cannot express are checked here. Both are
			// all-or-nothing per call: on either violation, nothing from this
			// call is added to the pending set.
			raw, _ := args["questions"].([]any)
			parsed := make([]askQuestion, 0, len(raw))
			for _, r := range raw {
				qm, _ := r.(map[string]any)
				labelsSeen := map[string]bool{}
				recommendedCount := 0
				opts, _ := qm["options"].([]any)
				for _, o := range opts {
					om, _ := o.(map[string]any)
					label := fmt.Sprint(om["label"])
					if labelsSeen[label] {
						return nil, errors.New("ask_user: option labels must be unique within a question")
					}
					labelsSeen[label] = true
					if rec, _ := om["recommended"].(bool); rec {
						recommendedCount++
					}
				}
				if recommendedCount > 1 {
					return nil, errors.New("ask_user: at most one option may be recommended")
				}
				parsed = append(parsed, askQuestion{
					Header:   fmt.Sprint(qm["header"]),
					Question: fmt.Sprint(qm["question"]),
				})
			}

			s.mu.Lock()
			s.askPending = append(s.askPending, parsed...)
			s.mu.Unlock()

			return askUserAckText, nil
		},
	})
}

// deriveRestoredState re-derives a restored session's at-rest state from its
// history tail (spec §6's pending definition): walking backward from the most
// recent turn, the first decisive turn wins. A TurnUserInput means a reply
// already resolved whatever was pending, or nothing ever was — idle. A
// TurnToolResults turn carrying a completed (non-error) ask_user result means
// that round ended its turn on an unanswered question — awaiting. Every other
// turn kind is not decisive and the scan continues past it: steering,
// checkpoint, summary, and system turns per spec, plus a TurnToolResults turn
// that carries no successful ask_user result. That last carve-out matters:
// when an ask_user call is interrupted before its ack is ever recorded,
// ResumeHistory's orphan repair (history_repair.go) synthesizes an IsError
// TOOL_RESULTS entry named "ask_user" so the provider never sees a dangling
// call — but that placeholder is not an ack, so it must not read as pending
// (spec §6: "an interrupted ack-less ask is never pending"). A denied or
// invalid ask_user call is IsError for the same reason and is excluded the
// same way. No decisive turn anywhere in the (possibly compacted) history
// defaults to idle, matching a fresh session.
func deriveRestoredState(history []schema.Turn) SessionState {
	for i := len(history) - 1; i >= 0; i-- {
		turn := history[i]
		switch turn.Kind {
		case schema.TurnUserInput:
			return SessionIdle
		case schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil &&
					part.ToolResult.Name == "ask_user" && !part.ToolResult.IsError {
					return SessionAwaiting
				}
			}
		}
	}
	return SessionIdle
}
