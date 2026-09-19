package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

type managedOccurrenceAnchor struct {
	AttemptGroupID string
	AssistantSeq   int
	Calls          []llm.ToolCallData
}
type managedCallPosition struct {
	Anchor    managedOccurrenceAnchor
	ToolIndex int
}
type managedCallPositionKey struct{}

func (s *Session) initManagedCatalog() error {
	if s.stateDir != "" && s.managedJournal == nil {
		j, err := openManagedJournal(filepath.Join(s.stateDir, sessionsSubdir, s.id+".managed.json"), s.id)
		if err != nil {
			return err
		}
		s.managedJournal = j
	}
	if s.cfg.ManagedRuntime == nil {
		return nil
	}
	catalog, catalogErr := s.cfg.ManagedRuntime.Catalog()
	s.managedBindingErr = catalogErr
	if catalogErr != nil {
		// Discovery failure must leave fresh ordinary sessions usable.
		catalog = nil
	}
	s.managedTools = map[string]ManagedTool{}
	for _, definition := range catalog {
		name := definition.Definition.Name
		if name == "" || definition.Operation == "" || definition.Validate == nil {
			return errors.New("incomplete managed tool definition")
		}
		if s.reg.Get(name) != nil {
			return fmt.Errorf("managed tool collides with registered tool %q", name)
		}
		definition.Definition.Parameters = tool.CloneSchemaMap(definition.Definition.Parameters)
		s.managedTools[name] = definition
		registered := tool.RegisteredTool{Definition: definition.Definition, ReadOnly: definition.ReadOnly, OmitIntent: true, ValidateRaw: definition.Validate,
			ExecRaw: func(ctx context.Context, _ execenv.ExecutionEnvironment, raw json.RawMessage) (any, error) {
				return s.executeManaged(ctx, name, raw)
			},
		}
		if err := s.reg.Register(registered); err != nil {
			return err
		}
	}
	s.reg.OverrideLimits(s.cfg.ToolOutputLimits)
	return nil
}
func (s *Session) bindManagedRuntime() {
	if s.cfg.ManagedRuntime == nil {
		return
	}
	names := []string{}
	for name := range s.managedTools {
		if s.isSubagentSession() && !slices.Contains(s.cfg.managedParentTools, name) {
			s.reg.Remove(name)
		}
		if s.reg.Get(name) != nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return
	}
	binding, err := s.cfg.ManagedRuntime.Bind(ManagedSession{SessionID: s.id, ParentSessionID: s.cfg.spawn.parentSessionID, Parent: s.cfg.managedParent, Tools: names})
	s.managedBinding, s.managedBindingErr = binding, err
	if err == nil && binding == nil {
		s.managedBindingErr = ErrManagedUnavailable
	}
}
func (s *Session) closeManagedBinding(ctx context.Context) {
	s.managedCloseOnce.Do(func() {
		if s.managedBinding != nil {
			budgetCtx, cancel := ensureCloseBudget(ctx)
			defer cancel()
			_ = s.managedBinding.Close(budgetCtx)
		}
	})
}
func (s *Session) hasManagedCalls(message llm.Message) bool {
	for _, part := range message.Content {
		if part.ToolCall != nil {
			if _, ok := s.managedTools[part.ToolCall.Name]; ok {
				return true
			}
		}
	}
	return false
}
func hasManagedResults(message llm.Message) bool {
	for _, part := range message.Content {
		if part.ToolResult != nil && part.ToolResult.ManagedInvocationID != "" {
			return true
		}
	}
	return false
}
func (s *Session) managedCallContext(ctx context.Context, index int) context.Context {
	s.mu.Lock()
	anchor := s.managedAnchor
	s.mu.Unlock()
	return context.WithValue(ctx, managedCallPositionKey{}, managedCallPosition{anchor, index})
}

// appendManagedTurn preserves the raw synced result while the existing paired
// append machinery adopts a retained entry exactly once and records fold data.
func (s *Session) appendManagedTurn(live, persisted schema.Turn) (int, error) {
	var seq int
	var rawErr error
	err := s.appendTurnAfterTranscriptWrite(persisted, func() error {
		s.mu.Lock()
		writer := s.transcript
		s.mu.Unlock()
		seq, rawErr = writer.AppendSyncedEntry(persisted)
		return rawErr
	}, func() { s.history = append(s.history, live) })
	return seq, errors.Join(err, rawErr)
}
func (s *Session) managedDurability() error {
	s.mu.Lock()
	writer := s.transcript
	s.mu.Unlock()
	if writer == nil {
		return transcript.ErrWriterClosed
	}
	if writer.Poisoned() {
		return transcript.ErrWriterPoisoned
	}
	return writer.EstablishDurability()
}
func (s *Session) executeManaged(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	definition, ok := s.managedTools[name]
	if !ok || s.reg.Get(name) == nil {
		return nil, ErrManagedAuthorityDenied
	}
	if s.managedBindingErr != nil {
		return nil, s.managedBindingErr
	}
	if s.managedBinding == nil {
		return nil, ErrManagedUnavailable
	}
	if err := definition.Validate(raw); err != nil {
		return nil, err
	}
	if s.managedJournal == nil || !s.hasTranscriptWriter() {
		if !definition.ReadOnly {
			return nil, errors.New("managed mutation requires durable session storage")
		}
		call := ManagedCall{ToolName: name, ToolCallID: managedToolCallID(ctx), Operation: definition.Operation, Arguments: raw}
		request, err := s.managedBinding.Prepare(ctx, call)
		if err != nil {
			return nil, err
		}
		if request.ToolName != call.ToolName || request.ToolCallID != call.ToolCallID || request.Operation != call.Operation || request.InvocationID != "" {
			return nil, errors.New("managed preparation changed operation identity")
		}
		if err := validateManagedIdentity(request.Identity); err != nil {
			return nil, err
		}
		if err := s.managedBinding.Authorize(ctx, request); err != nil {
			return nil, err
		}
		result, err := s.managedBinding.Execute(ctx, request)
		if err != nil {
			return nil, err
		}
		if err := validateManagedResult(result, request, s.id); err != nil {
			return nil, err
		}
		return tool.ManagedResult{Output: result.ModelText, Host: result.Host}, nil
	}
	position, ok := ctx.Value(managedCallPositionKey{}).(managedCallPosition)
	if !ok || position.Anchor.AttemptGroupID == "" || position.ToolIndex < 0 || position.ToolIndex >= len(position.Anchor.Calls) {
		return nil, errors.New("managed call has no durable assistant occurrence")
	}
	original := position.Anchor.Calls[position.ToolIndex]
	if original.Name != name {
		return nil, errors.New("managed occurrence tool mismatch")
	}
	if err := s.managedDurability(); err != nil {
		return nil, err
	}
	var invocation managedInvocation
	for _, candidate := range s.managedJournal.pending() {
		if candidate.AttemptGroupID == position.Anchor.AttemptGroupID && candidate.ToolIndex == position.ToolIndex {
			invocation = candidate
			break
		}
	}
	if invocation.ID == "" {
		invocation = managedInvocation{ID: identifier.MustNewAgentCallID(), SessionID: s.id, AttemptGroupID: position.Anchor.AttemptGroupID, ToolIndex: position.ToolIndex, AssistantSeq: position.Anchor.AssistantSeq, ToolName: name, CallID: original.ID, Arguments: append([]byte(nil), original.Arguments...)}
		request, err := s.managedBinding.Prepare(ctx, ManagedCall{ToolName: name, ToolCallID: original.ID, Operation: definition.Operation, InvocationID: invocation.ID, Arguments: append(json.RawMessage(nil), raw...)})
		if err != nil {
			return nil, err
		}
		if request.InvocationID != invocation.ID || request.Operation != definition.Operation || request.ToolName != name || request.ToolCallID != original.ID {
			return nil, errors.New("managed preparation changed operation identity")
		}
		invocation.Request = request
		if err := s.managedBinding.Authorize(ctx, request); err != nil {
			return nil, err
		}
		if err = s.managedJournal.put(invocation); err != nil {
			for _, pending := range s.managedJournal.pending() {
				if pending.ID == invocation.ID {
					return managedUnknown(invocation, err)
				}
			}
			return nil, err
		}
	} else {
		if invocation.ToolName != name || invocation.CallID != original.ID || !bytes.Equal(invocation.Arguments, original.Arguments) {
			return nil, errors.New("managed occurrence integrity mismatch")
		}
		if err := s.managedBinding.Authorize(ctx, invocation.Request); err != nil {
			return managedUnknown(invocation, err)
		}
		if err := s.managedJournal.establishDurability(); err != nil {
			return managedUnknown(invocation, err)
		}
	}
	if invocation.Result == nil {
		result, err := s.managedBinding.Execute(ctx, cloneManagedInvocation(invocation).Request)
		if err != nil {
			return managedUnknown(invocation, err)
		}
		if err = validateManagedResult(result, invocation.Request, s.id); err != nil {
			return managedUnknown(invocation, err)
		}
		invocation.Result = &result
		if err = s.managedJournal.put(invocation); err != nil {
			return managedUnknown(invocation, err)
		}
	}
	return tool.ManagedResult{Output: invocation.Result.ModelText, Host: invocation.Result.Host, InvocationID: invocation.ID}, nil
}
func managedUnknown(invocation managedInvocation, cause error) (any, error) {
	return tool.ManagedResult{Output: "Managed operation outcome is pending. It may have committed; this is not a rollback or rejection. The original invocation is retained for exact recovery.", InvocationID: invocation.ID}, cause
}
func validateManagedResult(result ManagedResult, request ManagedRequest, sessionID string) error {
	if result.Host != nil {
		origin := result.Host.Origin
		if result.Host.Version != 1 || origin.BindingID != request.Identity.BindingID || origin.ServiceID != request.Identity.ServiceID || origin.SessionID != sessionID {
			return errors.New("managed result origin does not match durable invocation")
		}
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > maxManagedResultBytes {
		return errors.New("invalid or oversized managed result")
	}
	return nil
}

// settleManagedTranscript retires only results whose exact invocation and host
// data exist in the real, synced transcript. An unknown pair is not settlement.
func (s *Session) settleManagedTranscript() error {
	if s.managedJournal == nil {
		return nil
	}
	if err := s.managedDurability(); err != nil {
		return err
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		return err
	}
	for _, invocation := range s.managedJournal.pending() {
		if invocation.Result == nil {
			continue
		}
		if managedTranscriptSettled(full.Entries, invocation) {
			if err := s.managedBindingAuthorize(context.Background(), invocation); err != nil {
				continue
			}
			if err := s.managedJournal.remove(invocation.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func managedTranscriptSettled(entries []transcript.Entry, invocation managedInvocation) bool {
	for _, entry := range entries {
		if resolution := entry.Turn.ManagedResolution; resolution != nil && resolution.InvocationID == invocation.ID {
			if equalManagedResult(ManagedResult{ModelText: resolution.ModelText, Host: resolution.MCPResult}, *invocation.Result) {
				return true
			}
		}
		for _, part := range entry.Turn.Message.Content {
			result := part.ToolResult
			if result != nil && result.ManagedInvocationID == invocation.ID && result.ToolCallID == invocation.CallID && result.Name == invocation.ToolName {
				if equalManagedResult(ManagedResult{ModelText: result.ManagedModelText, Host: result.MCPResult}, *invocation.Result) {
					return true
				}
			}
		}
	}
	return false
}
func equalManagedResult(a, b ManagedResult) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func (s *Session) managedBindingAuthorize(ctx context.Context, invocation managedInvocation) error {
	if s.reg.Get(invocation.ToolName) == nil {
		return ErrManagedAuthorityDenied
	}
	if s.managedBindingErr != nil {
		return s.managedBindingErr
	}
	if s.managedBinding == nil {
		return ErrManagedUnavailable
	}
	return s.managedBinding.Authorize(ctx, invocation.Request)
}

func (s *Session) appendManagedResolution(invocation managedInvocation) error {
	result := invocation.Result
	shaped := s.shapeManagedResult(invocation)
	turn := schema.NewTurn(schema.TurnSteering, llm.User(shaped.Output))
	turn.SteeringKind = events.SteeringKindNotification
	turn.ManagedResolution = &schema.ManagedResolutionInfo{InvocationID: invocation.ID, ModelText: result.ModelText, MCPResult: result.Host}
	_, err := s.appendManagedTurn(turn, turn)
	return err
}

func managedReservations(pending []managedInvocation) func(schema.Turn, int) bool {
	if len(pending) == 0 {
		return nil
	}
	return func(turn schema.Turn, index int) bool {
		for _, invocation := range pending {
			if turn.AttemptGroupID == invocation.AttemptGroupID && index == invocation.ToolIndex {
				return true
			}
		}
		return false
	}
}
func managedPaired(entries []transcript.Entry, invocation managedInvocation) bool {
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			r := part.ToolResult
			if r != nil && r.ManagedInvocationID == invocation.ID && r.ToolCallID == invocation.CallID && r.Name == invocation.ToolName {
				return true
			}
		}
	}
	return false
}
func managedAnchorPresent(entries []transcript.Entry, invocation managedInvocation) bool {
	foundOriginal := false
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind != schema.TurnAssistant || turn.AttemptGroupID != invocation.AttemptGroupID {
			continue
		}
		calls := assistantToolCalls(turn.Message)
		if invocation.ToolIndex >= len(calls) {
			return false
		}
		call := calls[invocation.ToolIndex]
		if call.ID != invocation.CallID || call.Name != invocation.ToolName || !sameManagedJSON(call.Arguments, invocation.Arguments) {
			return false
		}
		if entry.Seq == invocation.AssistantSeq {
			foundOriginal = true
		}
	}
	return foundOriginal
}
func (s *Session) managedPendingError(err error) *ManagedRecoveryPendingError {
	return &ManagedRecoveryPendingError{Denied: errors.Is(err, ErrManagedAuthorityDenied), Cause: err}
}
func (s *Session) checkManagedHistory() error {
	pending := s.managedJournal.pending()
	if len(pending) == 0 {
		return nil
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		return s.managedPendingError(err)
	}
	for _, invocation := range pending {
		if !managedPaired(full.Entries, invocation) {
			return s.managedPendingError(ErrManagedUnavailable)
		}
	}
	return nil
}

// reconcileManagedInvocations runs only at restore or an ordinary new drive.
// It never wakes a model, reruns hooks, or enters the model-callable registry.
func (s *Session) reconcileManagedInvocations(ctx context.Context) error {
	if len(s.managedJournal.pending()) == 0 {
		return nil
	}
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		return s.managedPendingError(err)
	}
	for _, invocation := range s.managedJournal.pending() {
		paired := managedPaired(full.Entries, invocation)
		if !managedAnchorPresent(full.Entries, invocation) {
			return s.managedPendingError(errors.New("managed assistant occurrence missing or changed"))
		}
		if err = s.managedBindingAuthorize(ctx, invocation); err != nil {
			if !paired {
				return s.managedPendingError(err)
			}
			continue
		}
		if err = s.managedDurability(); err != nil {
			return s.managedPendingError(err)
		}
		if err = s.managedJournal.establishDurability(); err != nil {
			if !paired {
				return s.managedPendingError(err)
			}
			continue
		}
		if invocation.Result == nil {
			result, executeErr := s.managedBinding.Execute(ctx, cloneManagedInvocation(invocation).Request)
			if executeErr == nil {
				executeErr = validateManagedResult(result, invocation.Request, s.id)
			}
			if executeErr != nil {
				if !paired {
					return s.managedPendingError(executeErr)
				}
				continue
			}
			invocation.Result = &result
			if err = s.managedJournal.put(invocation); err != nil {
				if !paired {
					return s.managedPendingError(err)
				}
				continue
			}
		}
		if managedTranscriptSettled(full.Entries, invocation) {
			if err = s.managedJournal.remove(invocation.ID); err != nil {
				return s.managedPendingError(err)
			}
			continue
		}
		if paired {
			err = s.appendManagedResolution(invocation)
		} else {
			result := invocation.Result
			shaped := s.shapeManagedResult(invocation)
			turn := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: invocation.CallID, Name: invocation.ToolName, Content: shaped.Output, ManagedModelText: result.ModelText, IsError: result.Host != nil && result.Host.IsError, MCPResult: result.Host, ManagedInvocationID: invocation.ID, ManagedAttemptGroupID: invocation.AttemptGroupID, ManagedToolIndex: invocation.ToolIndex}}}})
			_, err = s.appendManagedTurn(turn, turn)
		}
		if err != nil {
			return s.managedPendingError(err)
		}
		if err = s.managedJournal.remove(invocation.ID); err != nil {
			return s.managedPendingError(err)
		}
		full, err = readTranscriptFull(s.TranscriptPath())
		if err != nil {
			return s.managedPendingError(err)
		}
	}
	return nil
}

// sameManagedJSON uses the transcript encoder's JSON compaction and HTML
// escaping, without decoding numbers. Authored bytes remain exact in the journal.
func sameManagedJSON(a, b []byte) bool {
	x, err := json.Marshal(json.RawMessage(a))
	if err != nil {
		return false
	}
	y, err := json.Marshal(json.RawMessage(b))
	return err == nil && bytes.Equal(x, y)
}

func (s *Session) shapeManagedResult(invocation managedInvocation) tool.ExecResult {
	registered := s.reg.Get(invocation.ToolName)
	return tool.ShapeManagedResult(invocation.ToolName, invocation.CallID, registered.Limit, tool.ManagedResult{Output: invocation.Result.ModelText, Host: invocation.Result.Host, InvocationID: invocation.ID}, nil)
}

func managedToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(ctxToolCallID).(string)
	return id
}

func validateManagedIdentity(id ManagedIdentity) error {
	if id.BindingID == "" || id.ServiceID == "" || id.RealmID == "" || id.PrincipalID == "" || id.NamespaceID == "" {
		return errors.New("incomplete managed identity")
	}
	return nil
}
