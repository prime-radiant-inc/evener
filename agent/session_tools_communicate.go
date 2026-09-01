package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func callIDFromContext(ctx context.Context) string {
	callID, _ := ctx.Value(ctxToolCallID).(string)
	return callID
}

func registerCommunicateTool(reg *tool.Registry, deps *toolDeps) {
	// communicate is the only user-facing message channel.
	// Use the profile's definition if available (it may have been modified by
	// WithAllowedDecisions to add extra fields to the output schema).
	// Fall back to the base definition otherwise.
	resultToolDef := tool.DefCommunicateNamed(deps.resultToolName())
	if existing := reg.Get(deps.resultToolName()); existing != nil {
		resultToolDef = existing.Definition
	}
	_ = reg.Register(tool.RegisteredTool{
		Definition: resultToolDef,
		OmitIntent: true,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			message := ""
			if v, ok := args["message"]; ok {
				message = strings.TrimSpace(fmt.Sprint(v))
			}
			endTurn, ok := args["end_turn"].(bool)
			if !ok {
				return nil, errors.New("communicate requires end_turn")
			}

			originalOutput := normalizeNodeOutput(args["output"])
			if message == "" && strings.TrimSpace(originalOutput.Message) != "" {
				message = strings.TrimSpace(originalOutput.Message)
			}
			if message == "" {
				return nil, errors.New("communicate requires message or output.message")
			}
			explicitNodeOutput := hasMeaningfulNodeOutput(originalOutput)
			rawOutput, outputPresent := args["output"]
			usesCustomOutputSchema := !usesDefaultCommunicateOutputEnvelope(resultToolDef)
			explicitStructuredOutput := explicitNodeOutput || (outputPresent && usesCustomOutputSchema) || hasMeaningfulRawOutput(rawOutput)
			effectiveOutput := originalOutput
			if strings.TrimSpace(effectiveOutput.Message) == "" {
				effectiveOutput.Message = message
			}
			resultText := message
			structuredText := canonicalNodeOutputText(effectiveOutput)
			if explicitNodeOutput {
				resultText = structuredText
			}
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}

			deps.emit(events.EventCommunicate, events.CommunicateData{
				CallID:  callIDFromContext(ctx),
				EndTurn: endTurn,
				Message: message,
			})

			inbox := []string{}
			if endTurn {
				// Drain daemon-authored steering context into the terminal inbox.
				// Client-authored steering remains durable pending work for
				// wakeForPendingSteering and EntrySteeringCarrier: incorporating
				// it into a result that ends the turn would create a durable
				// transcript item without a model request that can act on it.
				// The inbox is text-only in the wire shape, so image-bearing
				// daemon entries are also appended as TurnSteering to keep their
				// ContentImage parts available to the next model round.
				drained := deps.drainSteering()
				inbox = make([]string, 0, len(drained))
				var deferred []steeringMessage
				for _, msg := range drained {
					if strings.TrimSpace(msg.Text) != "" {
						inbox = append(inbox, msg.Text)
					}
					if len(msg.Images) > 0 {
						deferred = append(deferred, msg)
					}
				}
				deps.prependSteering(deferred)
			}

			// end_turn=false calls never reach the terminal capture, so they stay
			// accepted; end_turn=true calls are accepted iff they won the capture.
			accepted := !endTurn
			if endTurn {
				// Atomic terminal capture (issue #570): the call's message,
				// canonical output, and raw structured value (when it carries
				// one) are handed to the setter together, so a later competing
				// terminal call can never pair its structured value with this
				// call's message — and a losing call reports accepted:false.
				var capturedOutput any
				if explicitStructuredOutput {
					capturedOutput = rawOutput
					if capturedOutput == nil && outputPresent {
						capturedOutput = json.RawMessage(`null`)
					}
				}
				accepted = deps.setCommunicateTerminal(ctx, message, resultText, structuredText, capturedOutput)
			}

			resp := map[string]any{
				"accepted": accepted,
				"end_turn": endTurn,
				"inbox":    inbox,
			}
			if endTurn && deps.runningJobIDs != nil {
				if ids := deps.runningJobIDs(); len(ids) > 0 {
					resp["warning"] = runningJobsEndTurnWarning(ids, deps.turnEndsProcess)
				}
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})
}

// runningJobsEndTurnWarning builds the end_turn=true warning naming this
// session's still-running managed jobs, detached processes, or live stable
// delegates. Warn-first (2026-08-06 ruling): the communicate call still
// succeeds, there is no refusal path.
//
// The promise the warning can make depends on whether the session outlives the
// turn. Where it does, the warning complements docs/job-control.md's
// notification contract rather than duplicating it — those jobs remain
// notification-armed and report on their own when they finish.
//
// Where turnEndsProcess holds, that promise is false: a one-shot `evener run`
// exits once the turn's work is drained, so a job that keeps running is killed
// rather than reported on. The drain gives such a job a further turn to be
// disposed of (see undisposedBackgroundJobsMessage), which is a remedy, not a
// reprieve — so this warning must not imply the job is safe.
func runningJobsEndTurnWarning(jobIDs []string, turnEndsProcess bool) string {
	detached := false
	delegate := false
	for _, id := range jobIDs {
		if strings.HasPrefix(id, "detached process ") {
			detached = true
		}
		if strings.HasPrefix(id, "delegate ") {
			delegate = true
		}
	}
	nouns := []string{"job(s)"}
	if detached {
		nouns = append(nouns, "detached process(es)")
	}
	if delegate {
		nouns = append(nouns, "delegate(s)")
	}
	noun := strings.Join(nouns, " or ")
	outcome := "each job remains notification-armed and will report separately on completion."
	if turnEndsProcess {
		outcome = "a job that finishes is reported in a further turn, but this run's process exits once that work is drained, so a job that keeps running is killed at exit rather than reported on later."
	}
	if detached {
		outcome = "a detached process has no completion notification, so its result is not collected by this run."
	}
	return fmt.Sprintf("ending turn while %d %s are still running: %s. The call still succeeds; %s",
		len(jobIDs), noun, strings.Join(jobIDs, ", "), outcome)
}

func registerSkillTool(reg *tool.Registry, deps *toolDeps) {
	// use_skill (progressive disclosure of skill instructions).
	// Present for provider profiles that include the use_skill tool definition.
	if reg.Get("use_skill") != nil {
		_ = reg.Register(tool.RegisteredTool{
			Definition: tool.DefUseSkill(),
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				_ = env
				skillName := fmt.Sprint(args["skill_name"])
				meta, ok := deps.skill(skillName)
				if !ok {
					return nil, fmt.Errorf("skill %q not found", skillName)
				}
				deps.emit(events.EventSkillActivated, events.SkillActivatedData{Name: skillName})
				body, err := skill.LoadSkillBody(meta)
				if err != nil {
					return nil, fmt.Errorf("loading skill %q: %w", skillName, err)
				}
				return systemNotificationf("Paths referenced in this skill are relative to the skill directory: %q", meta.Dir) + "\n\n" + body, nil
			},
		})
	}
}

type nodeOutput struct {
	Decision  string         `json:"decision,omitempty"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Artifacts []string       `json:"artifacts"`
}

func normalizeNodeOutput(raw any) nodeOutput {
	out := nodeOutput{
		Message:   "",
		Data:      map[string]any{},
		Artifacts: []string{},
	}
	if raw == nil {
		return out
	}
	if typed, ok := raw.(nodeOutput); ok {
		if typed.Data == nil {
			typed.Data = map[string]any{}
		}
		if typed.Artifacts == nil {
			typed.Artifacts = []string{}
		}
		return typed
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}

	if d, ok := m["decision"].(string); ok {
		out.Decision = d
	}
	if msg, ok := m["message"].(string); ok {
		out.Message = msg
	} else if v, ok := m["message"]; ok && v != nil {
		out.Message = fmt.Sprint(v)
	}
	if data, ok := m["data"].(map[string]any); ok {
		out.Data = data
	}
	if arts, ok := m["artifacts"]; ok {
		switch v := arts.(type) {
		case []string:
			out.Artifacts = append([]string{}, v...)
		case []any:
			out.Artifacts = make([]string, 0, len(v))
			for _, a := range v {
				out.Artifacts = append(out.Artifacts, fmt.Sprint(a))
			}
		}
	}
	return out
}

func hasMeaningfulNodeOutput(out nodeOutput) bool {
	return strings.TrimSpace(out.Decision) != "" ||
		strings.TrimSpace(out.Message) != "" ||
		len(out.Data) > 0 ||
		len(out.Artifacts) > 0
}

// defaultEnvelopeKeys are the output-envelope keys the default communicate
// schema declares — and the only keys the documented-defaults fill may add.
// Kept as one constant so the shape predicate and the fill cannot drift apart.
var defaultEnvelopeKeys = []string{"message", "data", "artifacts"}

// communicateEnvelopeFor reports whether t is the session's result tool with
// its default output envelope, returning that envelope schema when it is.
// This is the single owner of the fill's two gates (issue #627):
//   - identity: only the result tool gets the fill. A same-shaped schema on
//     any other registered tool (an MCP or plugin tool) must keep failing
//     loudly on keys the model was required to choose.
//   - exact shape: properties and required must each be precisely
//     defaultEnvelopeKeys — a custom output schema (a delegate result_schema
//     installed via WithCommunicateOutputSchema, or a WithAllowedDecisions
//     superset) keeps failing loudly.
//
// Returning the envelope it validated (rather than a bool the caller
// re-derives) is what keeps the check and the fill from diverging.
func communicateEnvelopeFor(t *tool.RegisteredTool, resultToolName string) (map[string]any, bool) {
	if t == nil || t.Definition.Name != resultToolName {
		return nil, false
	}
	props, _ := t.Definition.Parameters["properties"].(map[string]any)
	envelope, _ := props["output"].(map[string]any)
	outProps, _ := envelope["properties"].(map[string]any)
	if outProps == nil {
		return nil, false
	}
	required := communicateSchemaStringSlice(envelope["required"])
	if !stringSetsEqual(outProps, required, defaultEnvelopeKeys...) {
		return nil, false
	}
	return envelope, true
}

// usesDefaultCommunicateOutputEnvelope reports whether def's `output` property
// is exactly the default envelope DefCommunicateNamed builds: properties and
// required are each precisely {message, data, artifacts} — no more, no fewer.
// A superset (WithAllowedDecisions adds an enum-constrained `decision`) or a
// differently-shaped schema is a custom envelope, whose required keys the
// model was expected to choose.
func usesDefaultCommunicateOutputEnvelope(def llm.ToolDefinition) bool {
	props, _ := def.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if outProps == nil {
		return false
	}
	required := communicateSchemaStringSlice(output["required"])
	return stringSetsEqual(outProps, required, defaultEnvelopeKeys...)
}

// stringSetsEqual reports whether the schema's property names and required
// names are each exactly the wanted set (as sets: same members, same count).
func stringSetsEqual(props map[string]any, required []string, want ...string) bool {
	if len(props) != len(want) || len(required) != len(want) {
		return false
	}
	for _, name := range want {
		if _, ok := props[name]; !ok {
			return false
		}
		if !slices.Contains(required, name) {
			return false
		}
	}
	return true
}

func communicateSchemaStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// fillCommunicateEnvelope fills a present default-envelope `output` object's
// missing message/data/artifacts keys with their documented empty defaults
// ("" / {} / []). It mutates args in place on the working copy the caller
// owns, and never overwrites an existing key. Only communicateEnvelopeFor's
// envelope — the default one — may be passed here; a custom output schema
// must keep failing loudly on keys the model was required to choose.
func fillCommunicateEnvelope(envelope, args map[string]any) []repair.Change {
	raw, isMap := args["output"].(map[string]any)
	if !isMap {
		return nil
	}
	props, _ := envelope["properties"].(map[string]any)
	var changes []repair.Change
	for _, key := range defaultEnvelopeKeys {
		if _, present := raw[key]; present {
			continue
		}
		prop, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		v, ok := envelopeZeroValue(prop)
		if !ok {
			continue
		}
		raw[key] = v
		changes = append(changes, repair.Change{Kind: repair.ChangeFillRequired, Field: "output", Detail: "filled " + key})
	}
	return changes
}

// envelopeZeroValue returns the zero-value instance of an envelope property's
// declared type — the value a missing key is filled with. It returns ok=false
// for anything that is not a plain scalar, object, or array, and for
// enum-constrained properties: a zero value is never a value the model chose,
// so an enum field must stay absent and be reported as missing rather than
// silently sent as an invalid choice.
func envelopeZeroValue(prop map[string]any) (any, bool) {
	if _, hasEnum := prop["enum"]; hasEnum {
		return nil, false
	}
	typ, _ := prop["type"].(string)
	switch typ {
	case "string":
		return "", true
	case "boolean":
		return false, true
	case "integer", "number":
		return float64(0), true
	case "object":
		return map[string]any{}, true
	case "array":
		return []any{}, true
	}
	return nil, false
}

func hasMeaningfulRawOutput(raw any) bool {
	if raw == nil {
		return false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return true
	}
	for k, v := range m {
		switch k {
		case "decision", "message":
			if strings.TrimSpace(fmt.Sprint(v)) != "" {
				return true
			}
		case "data":
			if data, ok := v.(map[string]any); ok && len(data) == 0 {
				continue
			}
			if v != nil {
				return true
			}
		case "artifacts":
			switch arts := v.(type) {
			case []string:
				if len(arts) > 0 {
					return true
				}
			case []any:
				if len(arts) > 0 {
					return true
				}
			case nil:
			default:
				return true
			}
		default:
			return true
		}
	}
	return false
}

func canonicalNodeOutputText(raw any) string {
	return canonicalNodeOutputTextWithMarshal(raw, json.Marshal)
}

func canonicalNodeOutputTextWithMarshal(raw any, marshal func(any) ([]byte, error)) string {
	out := normalizeNodeOutput(raw)
	b, err := marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}
