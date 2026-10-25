package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

func callIDFromContext(ctx context.Context) string {
	callID, _ := ctx.Value(ctxToolCallID).(string)
	return callID
}

// registerCommunicateTool registers the session's result tool under its
// configured name, refusing a name this session cannot own (issue #4218:
// `--result-tool-name` could name a native tool and leave the session with no
// usable result tool).
//
// Callers register it LAST, after every other intrinsic tool, so the live
// registry — not a hand-maintained list that drifts as tools are added — is the
// oracle. Two collisions are possible:
//
//   - Another registered tool already holds the name. The profile-definition
//     adoption below and tool.Registry.Register's silent overwrite would give
//     the session a result tool with that tool's parameters and executor.
//   - The name is a registered tool's provider-visible alias. rebuildToolDefsCache
//     drops a registry tool whose wire name is already advertised, so the
//     result tool would never be advertised at all.
//   - The name is ask_user, whose behavior is keyed on the name string rather
//     than the registration: tool.Registry.executeCall normalizes arguments by
//     the call's name, and restore keys ask semantics on "ask_user" results.
//   - The name is in the memory_ namespace while this session has no memory.
//     filterUnavailableMemoryTools removes every memory_-prefixed name by
//     prefix, so even a name no tool currently holds — memory_future, say —
//     would be stripped after registration.
//
// The default name is exempt: adopting the profile's communicate definition,
// which is always named tool.DefaultResultToolName, is the point.
func registerCommunicateTool(reg *tool.Registry, deps *toolDeps) error {
	resultToolName := deps.resultToolName()
	if resultToolName != tool.DefaultResultToolName {
		if reg.Get(resultToolName) != nil {
			return fmt.Errorf("cannot use result tool name %q: an Evener tool of that name is already registered; choose a different --result-tool-name", resultToolName)
		}
		if deps.nativeWebSearch && resultToolName == "web_search" {
			return fmt.Errorf("cannot use result tool name %q: the provider's own web search tool uses that name for this profile; choose a different --result-tool-name", resultToolName)
		}
		for canonical, wire := range deps.toolNameMap {
			if wire == resultToolName && reg.Get(canonical) != nil {
				return fmt.Errorf("cannot use result tool name %q: it is this session's provider-visible name for Evener's %q tool; choose a different --result-tool-name", resultToolName, canonical)
			}
		}
		if resultToolName == askUserToolName {
			return fmt.Errorf("cannot use result tool name %q: that name is reserved for the ask_user question tool; choose a different --result-tool-name", resultToolName)
		}
		if deps.memoryToolsStripped && strings.HasPrefix(resultToolName, "memory_") {
			return fmt.Errorf("cannot use result tool name %q: this session has no memory namespace, so every memory_-prefixed tool name is removed; choose a different --result-tool-name", resultToolName)
		}
	}
	// communicate is the only user-facing message channel.
	// Profile overrides live on the canonical communicate definition, even when
	// this session renames the result tool. Adopt it before assigning the name so
	// output-schema replacement and allowed decisions survive the rename.
	resultToolDef := tool.DefCommunicateNamed(resultToolName)
	if existing := reg.Get(tool.DefaultResultToolName); existing != nil {
		resultToolDef = existing.Definition
		resultToolDef.Name = resultToolName
	}
	if deps.offersEndReason {
		resultToolDef = tool.WithCommunicateEndReason(resultToolDef)
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
			// The registry's schema has already held end_reason to its enum.
			endReason := ""
			if endTurn && deps.offersEndReason {
				endReason, _ = args["end_reason"].(string)
				if endReason == "" {
					endReason = tool.CommunicateEndReasonDone
				}
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

			if err := deps.deliverCommunicate(events.CommunicateData{
				CallID:    callIDFromContext(ctx),
				EndTurn:   endTurn,
				Message:   message,
				EndReason: endReason,
			}); err != nil {
				// The transcript refused the entry (a poisoned or closed
				// writer, or a served session failing closed): nothing
				// announced it, so the model must not be told it succeeded --
				// a captured terminal message it believes delivered would
				// otherwise never reach a client or history.
				return nil, err
			}

			// A delegate's end_turn=false message is an update for its parent.
			// The message is already in this transcript, so delivery never
			// fails the call; the result says where the update stands.
			var update *delegateUpdateDelivery
			if !endTurn && deps.postDelegateUpdate != nil {
				update = deps.postDelegateUpdate(ctx, message)
			}

			inbox := []string{}
			if endTurn {
				// Drain daemon-authored steering context into the terminal inbox.
				// Client-authored steering remains durable pending work for
				// wakeForPendingSteering and the steering carrier: incorporating
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
				accepted = deps.setCommunicateTerminal(ctx, message, resultText, structuredText, capturedOutput, endReason)
			}

			resp := map[string]any{
				"accepted": accepted,
				"end_turn": endTurn,
				"inbox":    inbox,
			}
			if update != nil {
				resp["delivery"] = update.Status
				if update.Reason != "" {
					resp["reason"] = update.Reason
				}
			}
			if endTurn && deps.runningJobIDs != nil {
				if ids := deps.runningJobIDs(); len(ids) > 0 {
					resp["warning"] = runningJobsEndTurnWarning(ids, deps.turnEndsProcess)
				}
			}
			b, _ := json.Marshal(resp)
			if accepted && endReason != "" {
				// The accepted end reason rides the result's tool state, which
				// restore reads (endedOnNeedsResponse): the result text can be
				// truncated or carry a repeated-call nudge.
				return tool.StateResult{Output: string(b), State: communicateEndState{EndReason: endReason}}, nil
			}
			return string(b), nil
		},
	})
	return nil
}

// communicateEndState is the tool state of the turn-ending communicate call
// the input accepted.
type communicateEndState struct {
	// The key is communicate's own, so no other tool's state can match it.
	EndReason string `json:"communicate_end_reason"`
}

// delegateUpdateDelivery is where a delegate's update stands when its
// communicate(end_turn=false) call returns: the result's delivery and reason.
type delegateUpdateDelivery struct {
	Status string
	Reason string
}

const (
	// The update is in the parent's transcript as attention.
	delegateUpdateDelivered = "delivered"
	// The update is durable in the delegate's journal and reaches the parent
	// without anyone acting: the parent holds it for its next model boundary,
	// or an earlier delivery of the same delegate is ahead of it.
	delegateUpdateQueued = "queued"
	// The journal refused the update; the message stays in the delegate's
	// own transcript only.
	delegateUpdateNotDelivered = "not_delivered"
)

// postDelegateUpdate hands a delegate's communicate(end_turn=false) message
// to its parent as an update. It returns nil for a root, whose end_turn=false
// message is a checkpoint for its human partner and goes nowhere else.
//
// An update carries the message and nothing else, the way a report does: it
// travels the delegate delivery path, which has never carried a watch's causal
// provenance. A feedback loop between an observer and the parent it watches is
// bounded the same way for updates as for reports: the update is a communicate
// event, which the watch on the sender counts toward watchDeliveryBudget, and
// the watch auto-clears at the budget (TestWatchDeliveryCounterAndBudget).
func (s *Session) postDelegateUpdate(ctx context.Context, message string) *delegateUpdateDelivery {
	if !s.reportsToParent() {
		return nil
	}
	if s.delegateController == nil || s.owningDelegateID == "" {
		return &delegateUpdateDelivery{Status: delegateUpdateNotDelivered, Reason: "this session has no route to a parent agent"}
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return notDeliveredUpdate(err)
	}
	deliveryID, plans, err := s.delegateController.PostUpdate(actor, message)
	if err != nil {
		return notDeliveredUpdate(err)
	}
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		// The update is durable in the journal. A delivery that failed stays
		// pending there, and executeDelegateMutationPlans has queued the
		// journal's pending heads and the rest of its queue for retry.
		s.emit(events.EventWarning, warningDataFromError("deliver delegate update", err))
	}
	// A missing aggregate reads as not pending: it cannot occur while the run
	// that just posted is open.
	if s.delegateController.deliveryPending(s.owningDelegateID, deliveryID) {
		return &delegateUpdateDelivery{Status: delegateUpdateQueued}
	}
	return &delegateUpdateDelivery{Status: delegateUpdateDelivered}
}

// notDeliveredUpdate says, for the delegate's model, why its update was
// refused.
func notDeliveredUpdate(err error) *delegateUpdateDelivery {
	reason := "it could not be recorded: " + err.Error()
	switch {
	case errors.Is(err, errDelegateStaleLease):
		reason = "this run of yours is no longer the current one"
	case errors.Is(err, errDelegateTargetBusy), errors.Is(err, errDelegateNotControllable), errors.Is(err, ErrRetirementUnavailable):
		reason = "you are stopping or finishing, or this run can no longer report to your parent"
	}
	return &delegateUpdateDelivery{Status: delegateUpdateNotDelivered, Reason: reason}
}

// runningJobsEndTurnWarning builds the end_turn=true warning naming this
// session's still-running managed jobs or detached processes. Warn-first (2026-08-06 ruling): the
// communicate call still succeeds, there is no refusal path.
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
	for _, id := range jobIDs {
		if strings.HasPrefix(id, "detached process ") {
			detached = true
			break
		}
	}
	noun := "job(s)"
	if detached {
		noun = "job(s) or detached process(es)"
	}
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
				_ = env
				return deps.skillActivate(ctx, fmt.Sprint(args["skill_name"]), callIDFromContext(ctx))
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
var defaultEnvelopeKeys = []string{"message", "data", "artifacts"}

// canonicalDefaultCommunicateOutputSchema is constructed once because every
// communicate repair checks it, sometimes multiple times. It is private and
// only read by reflect.DeepEqual; callers never receive this mutable map.
var canonicalDefaultCommunicateOutputSchema = func() map[string]any {
	parameters := tool.DefCommunicateNamed("communicate").Parameters
	properties, _ := parameters["properties"].(map[string]any)
	output, _ := properties["output"].(map[string]any)
	return output
}()

// communicateEnvelopeFor reports whether t is the session's result tool with
// the exact canonical default output envelope, returning that envelope schema
// when it is.
// This is the single owner of the fill's two gates (issue #627):
//   - identity: only the result tool gets the fill. A same-shaped schema on
//     any other registered tool (an MCP or plugin tool) must keep failing
//     loudly on keys the model was required to choose.
//   - exact schema: equality with DefCommunicateNamed's canonical output
//     schema. Same-key schemas can carry stricter types, enums, descriptions,
//     or other constraints, so key-name comparison is not sufficient.
//
// Returning the envelope it validated (rather than a bool the caller
// re-derives) is what keeps the check and the fill from diverging.
func communicateEnvelopeFor(t *tool.RegisteredTool, resultToolName string) (map[string]any, bool) {
	if t == nil || t.Definition.Name != resultToolName {
		return nil, false
	}
	props, _ := t.Definition.Parameters["properties"].(map[string]any)
	envelope, _ := props["output"].(map[string]any)
	if !isCanonicalDefaultCommunicateOutputEnvelope(envelope) {
		return nil, false
	}
	return envelope, true
}

// usesDefaultCommunicateOutputEnvelope reports whether def's `output` property
// equals the complete canonical output schema DefCommunicateNamed builds. A
// superset, a same-key schema with a stricter enum, or any other variation is a
// custom envelope whose fields must remain exact.
func usesDefaultCommunicateOutputEnvelope(def llm.ToolDefinition) bool {
	props, _ := def.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	return isCanonicalDefaultCommunicateOutputEnvelope(output)
}

func isCanonicalDefaultCommunicateOutputEnvelope(output map[string]any) bool {
	return reflect.DeepEqual(output, canonicalDefaultCommunicateOutputSchema)
}

// fillCommunicateEnvelope fills a present default-envelope `output` object's
// missing message/data/artifacts keys with their documented empty defaults
// ("" / {} / []). It mutates args in place on the working copy the caller
// owns, and never overwrites an existing key. Only communicateEnvelopeFor's
// envelope — the default one — may be passed here; repairDefaultCommunicateEnvelope
// owns that canonical-schema gate before calling this helper. A custom output
// schema must keep failing loudly on keys the model was required to choose.
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
		changes = append(changes, repair.Change{Kind: repair.ChangeFillRequired, Field: "output." + key, Detail: "filled default"})
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
