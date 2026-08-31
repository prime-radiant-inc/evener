package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

const (
	maxJobOutputRetentionBytes  = 8 * 1024 * 1024
	defaultJobListLimit         = 50
	maxJobListLimit             = 100
	minJobBlockTimeoutMS        = 1000
	maxJobBlockTimeoutMS        = 60000
	jobToolResultDefaultMaxChar = 20_000
	jobToolResultMinJSONChars   = 800
	jobManagerUnavailableReason = "job manager is not available"
	maxJobGrepMatches           = 100
	maxJobGrepLineBytes         = 4096
	jobKindAgent                = "agent"
	jobKindShell                = "shell"
	jobPhaseStarting            = "starting"
	jobPhaseAwaitingModel       = "awaiting_model"
	jobPhaseModelStreaming      = "model_streaming"
	jobPhaseModelRetrying       = "model_retrying"
	jobPhaseToolRunning         = "tool_running"
	jobPhaseProcessRunning      = "process_running"
)

// clampJobBlockTimeout clamps a positive max_wait_ms request into the supported
// inline-block window [minJobBlockTimeoutMS, maxJobBlockTimeoutMS] and returns
// it as a wait duration. Callers gate on ms > 0 (and reject negatives) upstream.
func clampJobBlockTimeout(ms int) time.Duration {
	clamped := max(ms, minJobBlockTimeoutMS)
	clamped = min(clamped, maxJobBlockTimeoutMS)
	return time.Duration(clamped) * time.Millisecond
}

// rootOnlyJobControlTools gates DELEGATION, not job control: a session that
// can run jobs can always watch its own jobs, at any depth, so job_watch is
// deliberately not here. The accesses the old job_watch strip was protecting
// are each enforced at their own source: `parent` requires
// delegate(watch_parent=true) (delegate_tree_watch.go), and a concrete job id
// must be owned by the watching session (job_watch.go's target_not_watchable).
var rootOnlyJobControlTools = []string{"delegate"}

func registerJobTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	if deps != nil && deps.registerTool != nil {
		return registerJobToolsWithRegistrar(reg, jobToolRegisterFunc(func(registered tool.RegisteredTool) error {
			return deps.registerTool(reg, registered)
		}), s, deps)
	}
	return registerJobToolsWithRegistrar(reg, reg, s, deps)
}

type jobToolRegisterFunc func(tool.RegisteredTool) error

func (f jobToolRegisterFunc) Register(registered tool.RegisteredTool) error { return f(registered) }

type jobToolRegistrar interface {
	Register(tool.RegisteredTool) error
}

// registerJobToolsWithRegistrar keeps production registration on Registry while
// allowing deterministic failure injection at each registration boundary.
func registerJobToolsWithRegistrar(reg *tool.Registry, registrar jobToolRegistrar, s *Session, deps *toolDeps) error {
	_ = deps
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobStatus(), ReadOnly: true,
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobStatusTool(s, args, jobToolResultMaxChars(reg, "job_status"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobList(), ReadOnly: true,
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobListTool(s, args, jobToolResultMaxChars(reg, "job_list"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobStop(),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobStopTool(ctx, s, args, jobToolResultMaxChars(reg, "job_stop"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobWatch(availableEventKindNames()),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobWatchToolWithContext(ctx, s, args, jobToolResultMaxChars(reg, "job_watch"))
		},
	}); err != nil {
		return err
	}
	return nil
}

// liveSteerWaitIgnoredReason returns a note when a caller passed a positive
// max_wait_ms but the send was a live steer of a running delegate, which returns on
// delivery and cannot honor the wait. It returns "" when the wait was honored (a
// resumed job) or not requested, so the field stays omitted in the common case.
func liveSteerWaitIgnoredReason(blockTimeoutMS int, status jobstore.Status, action string) string {
	if blockTimeoutMS > 0 && (status == jobstore.StatusRunning && action == "steered" || action == "delivered") {
		return "live steer returns on delivery; max_wait_ms applies only to started jobs"
	}
	return ""
}

func jobWatchTool(s *Session, args map[string]any, maxChars int) (any, error) {
	return jobWatchToolWithContext(context.Background(), s, args, maxChars)
}

func jobWatchToolWithContext(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	a, err := watchArgsFromToolArgs(args)
	if err != nil {
		return "", err
	}
	var res watchResult
	switch a.Operation {
	case "create":
		var source watchSource
		source, err = normalizeWatchSource(a.Source)
		if err != nil {
			return "", err
		}
		if source.Kind == watchSourceParentSession {
			if s.delegateController == nil {
				return "", errors.New("source_not_watchable: stable delegate controller is unavailable")
			}
			actor, actorErr := s.delegateActor(ctx)
			if actorErr != nil {
				return "", actorErr
			}
			binding, bindErr := s.delegateController.ResolveParentWatchSource(actor)
			if bindErr != nil {
				return "", bindErr
			}
			res, err = s.configureStableWatchOnSource("parent", binding, a)
			break
		}
		if source.Kind == watchSourceStableDelegate {
			if s.delegateController == nil {
				return "", errors.New("delegate controller is unavailable")
			}
			actor, actorErr := s.delegateActor(ctx)
			if actorErr != nil {
				return "", actorErr
			}
			binding, bindErr := s.delegateController.ResolveStableWatchSource(actor, source.Public)
			if bindErr != nil {
				return "", bindErr
			}
			res, err = s.configureStableWatchOnSource(source.Public, binding, a)
			break
		}
		a.Source = source.Public
		a.Target = source.Internal
		if source.Kind == watchSourceConcreteJob {
			if ownerRes, forwarded, ownerErr := s.configureDescendantReceiverWatch(a); forwarded || ownerErr != nil {
				res, err = ownerRes, ownerErr
				break
			}
		}
		res, err = jm.configureWatch(a)
	case "clear":
		local, localErr := jm.hasWatchID(a.WatchID)
		if localErr != nil {
			return "", localErr
		}
		if local {
			res, err = jm.clearWatchByID(a.WatchID)
			break
		}
		if ownerRes, found, ownerErr := s.clearStableReceiverWatchByID(a.WatchID); found || ownerErr != nil {
			res, err = ownerRes, ownerErr
			break
		}
		// #655: the watch may still live in this manager keyed to another
		// session's receiver (hasWatchID reports the visibility verdict, not
		// presence), or in a descendant's manager the receiver-path above did
		// not cover. Either way the clear is authorized by topology — the
		// receiver must be this session's own delegate or a descendant of it —
		// never by source labels: a source delegate must not be able to clear
		// its ancestor's watch on it, and a sibling must not clear another
		// sibling's.
		if receiverSession, receiverDelegate, ok := s.receiverWatchAnywhereByID(a.WatchID); ok {
			if s.delegateController == nil || !s.delegateController.watchClearAuthority(s.ID(), s.owningDelegateID, receiverDelegate) {
				receiver := receiverDelegate
				if receiver == "" {
					receiver = "session " + receiverSession
				}
				return "", fmt.Errorf("invalid_request: watch %s delivers to %s, which this session may not clear", a.WatchID, receiver)
			}
			holder := s.jobManagerHoldingWatch(a.WatchID)
			if holder == nil {
				holder = jm
			}
			res, err = holder.clearReceiverWatchByID(a.WatchID, receiverSession, receiverDelegate)
			break
		}
		res, err = jm.clearWatchByID(a.WatchID)
	case "list":
		return marshalWatchListResult(s.watchListToolResultWithDescendantReceivers(jm.watchListToolResult()), maxChars)
	case "inspect":
		inspect := jm.inspectWatchByID(a.WatchID)
		if watchInspectFound(inspect) {
			return marshalWatchInspectResult(inspect, maxChars)
		}
		if ownerInspect, found := s.inspectDescendantReceiverWatchByID(a.WatchID); found {
			return marshalWatchInspectResult(ownerInspect, maxChars)
		}
		return marshalWatchInspectResult(inspect, maxChars)
	}
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}

func (s *Session) configureStableWatchOnSource(sourcePublic string, binding delegateWatchSourceBinding, a watchArgs) (watchResult, error) {
	if s == nil || binding.runtime == nil || binding.runtime.jobManager == nil || s.delegateController == nil {
		return watchResult{}, errors.New("source_not_watchable: stable watch source is unavailable")
	}
	a.Source = sourcePublic
	a.Target = runtimeMessageAliasCaller
	a.ReceiverSessionID = s.ID()
	a.ReceiverDelegateID = s.owningDelegateID
	a.StableReceiver = true
	a.ReceiverSendInternal = true
	if binding.lease != nil {
		a.SourceDelegateID = binding.lease.delegateID
		a.SourceGeneration = binding.lease.generation
	}
	return binding.runtime.jobManager.configureWatch(a)
}
func (s *Session) configureDescendantReceiverWatch(a watchArgs) (watchResult, bool, error) {
	if s == nil || !strings.HasPrefix(a.Target, "job_") {
		return watchResult{}, false, nil
	}
	ownerJM, ownerSess, _, _, ok := s.resolveDescendantJobOwner(a.Target)
	if !ok || ownerJM == nil || ownerSess == nil {
		return watchResult{}, false, nil
	}
	childArgs := a
	childArgs.Source = a.Source
	childArgs.Target = a.Target
	childArgs.ReceiverSessionID = s.ID()
	childArgs.ReceiverDelegateID = ""
	childArgs.ReceiverNotify = s.enqueueJobNotificationAndNotify
	childArgs.ReceiverHoldWake = s.holdJobNotificationWake
	res, err := ownerJM.configureWatch(childArgs)
	return res, true, err
}

func (s *Session) watchListToolResultWithDescendantReceivers(local jobWatchListToolResult) jobWatchListToolResult {
	receiverDelegateID := s.owningDelegateID
	for _, child := range s.stableWatchSourceSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		descendant := child.jobManager.watchListToolResultForReceiver(s.ID(), receiverDelegateID)
		local.Watches = append(local.Watches, descendant.Watches...)
		local.RecentWatches = append(local.RecentWatches, descendant.RecentWatches...)
	}
	sort.SliceStable(local.Watches, func(i, j int) bool {
		if local.Watches[i].Source != local.Watches[j].Source {
			return local.Watches[i].Source < local.Watches[j].Source
		}
		return local.Watches[i].WatchID < local.Watches[j].WatchID
	})
	local.Count = len(local.Watches)
	return local
}

func (s *Session) inspectDescendantReceiverWatchByID(watchID string) (jobWatchInspectToolResult, bool) {
	receiverDelegateID := s.owningDelegateID
	for _, child := range s.stableWatchSourceSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		if inspect, ok := child.jobManager.inspectReceiverWatchByID(watchID, s.ID(), receiverDelegateID); ok {
			return inspect, true
		}
	}
	return jobWatchInspectToolResult{}, false
}

//nolint:unused // retained for tagged stable receiver-watch state-machine owners.
func (s *Session) clearDescendantReceiverWatchByID(watchID string) (watchResult, bool, error) {
	return s.clearStableReceiverWatchByID(watchID)
}

func (s *Session) clearStableReceiverWatchByID(watchID string) (watchResult, bool, error) {
	receiverDelegateID := s.owningDelegateID
	for _, child := range s.stableWatchSourceSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		if _, ok := child.jobManager.inspectReceiverWatchByID(watchID, s.ID(), receiverDelegateID); !ok {
			continue
		}
		res, err := child.jobManager.clearReceiverWatchByID(watchID, s.ID(), receiverDelegateID)
		return res, true, err
	}
	return watchResult{}, false, nil
}

// receiverWatchAnywhereByID finds a watch's receiver identity across this
// session's own manager and the watch-source sessions, ignoring visibility.
// Returns ok=false when no manager holds the watch.
func (s *Session) receiverWatchAnywhereByID(watchID string) (receiverSessionID, receiverDelegateID string, found bool) {
	if s == nil || watchID == "" {
		return "", "", false
	}
	if s.jobManager != nil {
		if receiverSession, receiverDelegate, ok := s.jobManager.watchReceiverIdentity(watchID); ok {
			return receiverSession, receiverDelegate, true
		}
	}
	for _, holder := range s.stableWatchSourceSessions() {
		if holder == nil || holder.jobManager == nil || holder.jobManager == s.jobManager {
			continue
		}
		if receiverSession, receiverDelegate, ok := holder.jobManager.watchReceiverIdentity(watchID); ok {
			return receiverSession, receiverDelegate, true
		}
	}
	return "", "", false
}

// jobManagerHoldingWatch returns the manager (of this session or the watch
// source sessions) that currently holds the watch, or nil when none does.
func (s *Session) jobManagerHoldingWatch(watchID string) *jobManager {
	if s == nil || watchID == "" {
		return nil
	}
	holders := make([]*jobManager, 0, 2)
	if s.jobManager != nil {
		holders = append(holders, s.jobManager)
	}
	for _, holder := range s.stableWatchSourceSessions() {
		if holder != nil && holder.jobManager != nil && holder.jobManager != s.jobManager {
			holders = append(holders, holder.jobManager)
		}
	}
	for _, holder := range holders {
		if _, _, found := holder.watchReceiverIdentity(watchID); found {
			return holder
		}
	}
	return nil
}

func (s *Session) stableWatchSourceSessions() []*Session {
	if s == nil || s.delegateController == nil {
		return s.liveDescendantSessions()
	}
	return s.delegateController.watchSourceSessions()
}

// liveWatchesDeliveringToDelegate returns the armed watches that deliver to a
// delegate and the members of the subtree rooted at it, keyed by receiver
// identity — the #655 inventory. The stopper (parent) session is always a
// member of stableWatchSourceSessions(), and the parent-source watch an
// observer child installs lives in that same manager
// (configureStableWatchOnSource), so one scan covers it without
// double-reporting. Receiver keys come from the durable descriptors, readable
// before, during, and after a stop. Managers are deduped per delegate: two
// live sessions can share one job manager, and a naive per-session append
// would double every row.
//
// Boundary: the scan covers managers whose runtimes are currently live
// (stableWatchSourceSessions), plus the stopper's own. A subtree member whose
// owning session is not live (a cold, idle descendant that no live runtime
// registers) is not scanned — a watch held only in that member's cold manager
// is not reported. The receiver key itself is durable, so watches held in any
// live manager (including the stopper's) that deliver to a cold member ARE
// reported.
func (s *Session) liveWatchesDeliveringToDelegate(delegateID string) []watchListEntry {
	if s == nil || delegateID == "" || s.delegateController == nil {
		return nil
	}
	receiverKeys := s.delegateController.subtreeReceiverKeysForDelegate(delegateID)
	if len(receiverKeys) == 0 {
		return nil
	}
	var entries []watchListEntry
	seenManagers := make(map[*jobManager]struct{})
	for _, holder := range s.stableWatchSourceSessions() {
		if holder == nil || holder.jobManager == nil {
			continue
		}
		if _, scanned := seenManagers[holder.jobManager]; scanned {
			continue
		}
		seenManagers[holder.jobManager] = struct{}{}
		for childDelegateID, childSessionID := range receiverKeys {
			entries = append(entries, holder.jobManager.liveWatchSummariesForReceiver(childSessionID, childDelegateID)...)
		}
	}
	sort.SliceStable(entries, watchListEntryLess(entries))
	return entries
}

func (s *Session) liveDescendantSessions() []*Session {
	if s == nil {
		return nil
	}
	var out []*Session
	for _, child := range s.liveSubagentSessions() {
		out = append(out, child)
		out = append(out, child.liveDescendantSessions()...)
	}
	return out
}

func watchInspectFound(inspect jobWatchInspectToolResult) bool {
	return inspect.Watching || inspect.Source != "" || inspect.EndReason != ""
}

// decodeDelegateArgs decodes the delegate tool's raw params into delegateArgs,
// returning an invalid_request error for a malformed wait/allowance value. It is
// pure over the args map (no session state), so the decode — including the
// tri-state sandbox_net (nil = inherit, never a silent false) — is unit-testable
// without minting a delegate.
func decodeDelegateArgs(args map[string]any) (delegateArgs, error) {
	a := delegateArgs{
		Task:            stringArg(args, "task"),
		AgentType:       stringArg(args, "agent_type"),
		Model:           stringArg(args, "model"),
		ReasoningEffort: stringArg(args, "reasoning_effort"),
		WatchParent:     shellBoolArg(args, "watch_parent"),
		Isolation:       stringArg(args, "isolation"),
		Sandbox:         stringArg(args, "sandbox"), // may carry "+nonet" suffix or be "nonet" alone
	}
	// The sandbox field now encodes both mode and an optional network override
	// in a single enum value. Values like "read-only+nonet" split into mode=
	// "read-only" and sandbox_net=false. "nonet" alone means inherit the
	// parent's mode and disable network. The legacy separate sandbox_net
	// boolean field is no longer part of the schema, but we still accept it
	// from in-process callers that build args directly (tests, scripted
	// providers) so existing test fixtures don't break.
	sandboxVal := strings.TrimSpace(a.Sandbox)
	if sandboxVal == "nonet" || strings.HasSuffix(sandboxVal, "+nonet") {
		// "nonet" alone (inherit parent mode, disable network) and any
		// "<mode>+nonet" suffix both decode to a net-off request. The bare
		// "nonet" value is advertised by the combined enum for non-off
		// parents, so it must split here exactly like "+nonet".
		mode := strings.TrimSuffix(sandboxVal, "+nonet")
		if mode == "nonet" {
			mode = ""
		}
		a.Sandbox = mode
		netFalse := false
		a.SandboxNet = &netFalse
	}
	if rawNet, exists := args["sandbox_net"]; exists {
		switch v := rawNet.(type) {
		case bool:
			a.SandboxNet = &v
		default:
			return delegateArgs{}, errors.New("invalid_request: sandbox_net must be a JSON boolean (true or false, not a quoted string)")
		}
	}
	// delegation_allowance: 0/absent = leaf delegate (cannot delegate); positive
	// = grant; negative = invalid_request. Zero reads as unset (strict-zero rule).
	// createDelegate enforces the grant rule (strictly less than own allowance).
	if n, ok := shellIntArg(args, "delegation_allowance"); ok && n != 0 {
		if n < 0 {
			return delegateArgs{}, errors.New("invalid_request: delegation_allowance must be non-negative")
		}
		a.DelegationAllowance = n
	}
	if resultSchema, ok := args["result_schema"].(map[string]any); ok {
		a.ResultSchema = resultSchema
	}
	return a, nil
}

func jobStatusTool(s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "target"))
	if target == "" {
		return "", errors.New("invalid_request: target is required")
	}
	if strings.HasPrefix(target, "dlg_") {
		return stableDelegateStatusTool(s, target, maxChars)
	}
	jm, rec, err := s.nestedOrLocalJobManager(target)
	if err != nil {
		return "", err
	}
	if string(rec.Type) == delegateResourceType {
		return "", fmt.Errorf("legacy_delegate_activation: %s is a retired delegate activation alias; use its stable delegate_id", target)
	}
	if live, liveErr := findJobRecord(jm, target); liveErr == nil {
		rec = live
	}
	out := projectJobStatus(jm.now(), rec)
	rendered, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: rendered, State: out}, nil
}

func stableDelegateStatusTool(s *Session, delegateID string, maxChars int) (any, error) {
	if s == nil || s.delegateController == nil {
		return "", errors.New("delegate controller is unavailable")
	}
	rows := stableDelegateRowsForSession(s, true)
	var row *stableDelegateVisibleRow
	for i := range rows {
		if rows[i].snapshot.id == delegateID {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return "", fmt.Errorf("not_controllable: stable delegate %s is not visible to this session", delegateID)
	}
	now := s.sclock().Now()
	out := projectStableDelegateStatus(now, row.snapshot)
	rendered, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: rendered, State: out}, nil
}

// consumeTerminalJobNotification settles the terminal notification after its
// job_status result is durable in the session transcript. At that point the
// caller has learned the job ended, so waking it later to say the same thing is
// an interruption that carries no news.
//
// It is recorded durably as its own state (consumed, not delivered) so the
// told-the-caller invariant stays true without claiming a notification turn
// that never happened — evener-doctor can still tell the two apart.
//
// Only the OWNER's own reads consume. A parent's forwarded copy of a
// child-owned pending is a drive signal, not the parent's news to hear:
// settling it there would silence the child's own undelivered notification.
func consumeTerminalJobNotification(s *Session, jm *jobManager, rec *jobstore.JobRecord) {
	markTerminalJobNotificationConsumed(s, jm, rec, true)
}

// markTerminalJobNotificationConsumed persists the consumed disposition and,
// when removeQueued is true, applies the ordinary status-read behavior of
// removing matching queue entries. Terminal-drain cuts pass false: they have
// already removed only the exact pre-cut queue identities, and removing by job
// ID here could erase a fresh post-cut generation of the same job.
func markTerminalJobNotificationConsumed(s *Session, jm *jobManager, rec *jobstore.JobRecord, removeQueued bool) {
	if jm == nil || rec == nil || rec.TerminalGen == "" {
		return
	}
	if !rec.Status.IsTerminal() || rec.NotifyState != jobstore.NotifyPending {
		return
	}
	ownerSessionID := rec.OwnerSessionID
	if ownerSessionID == "" {
		ownerSessionID = jm.sessionID
	}
	if ownerSessionID != s.id {
		return
	}
	consumed := jobstore.Event{
		Kind:        jobstore.EventJobNotificationConsumed,
		TS:          jm.now(),
		JobID:       rec.JobID,
		TerminalGen: rec.TerminalGen,
	}
	if err := jm.appendEvent(consumed); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification consume mark failed: %v", err)})
		return
	}
	rec.NotifyState = jobstore.NotifyConsumed
	if removeQueued && jm.consume != nil {
		jm.consume(rec.JobID)
	}
	// Settle the parent's forwarded COPY too, for the same reason a delivery
	// does: the copy is only a drive signal, and a signal nobody clears
	// re-drives forever.
	if err := jm.forwardSnapshot(consumed); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification consume forward failed: %v", err)})
	}
}

func jobListTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	filter, err := jobListFilterFromArgs(args)
	if err != nil {
		return "", err
	}
	now := s.sclock().Now()
	items, err := stableJobListCandidates(s, jm, filter, now)
	if err != nil {
		return "", err
	}
	sort.Slice(items, func(i, j int) bool {
		left := jobListItemActivity(items[i])
		right := jobListItemActivity(items[j])
		if left.Equal(right) {
			return items[i].ID < items[j].ID
		}
		return left.After(right)
	})
	total := len(items)
	if filter.Offset > 0 {
		if filter.Offset >= len(items) {
			items = nil
		} else {
			items = items[filter.Offset:]
		}
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	s.mu.Lock()
	allowance := s.delegationAllowance
	s.mu.Unlock()
	result := jobListResult{
		Items:         items,
		Count:         len(items),
		Offset:        filter.Offset,
		Total:         total,
		TurnSlots:     turnSlotOccupancyOf(s),
		Watches:       jm.liveWatchSummaries(),
		RecentWatches: jm.recentWatchSummaries(),
	}
	// Allowance ≤ 1 can only grant 0 — a no-op knob; surface it only when it
	// actually enables fan-out (mirrors eliding the delegate schema param).
	if allowance > 1 {
		result.DelegationAllowance = allowance
	}
	_ = maxChars
	return tool.StateResult{Output: formatJobList(result), State: result}, nil
}

func stableJobListCandidates(s *Session, jm *jobManager, filter listFilter, now time.Time) ([]jobListEntry, error) {
	shellFilter := filter
	shellFilter.Status = ""
	shellFilter.Statuses = nil
	shellFilter.Type = ""
	shellFilter.Types = nil
	shellFilter.Offset = 0
	shellFilter.Limit = 0

	var shellItems []jobListEntry
	if filter.IncludeDescendants {
		rows, _, err := s.walkDescendantJobs(shellFilter)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Type != string(jobstore.JobShell) {
				continue
			}
			row.ID = row.JobID
			shellItems = append(shellItems, row)
		}
	} else {
		records, _, err := jm.listWithError(shellFilter)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if record.Type != jobstore.JobShell {
				continue
			}
			row := projectJobRecordAt(s, s, record, now)
			row.ID = row.JobID
			shellItems = append(shellItems, row)
		}
	}

	items := make([]jobListEntry, 0, len(shellItems))
	for _, item := range shellItems {
		if stableJobListItemMatches(item, filter) {
			items = append(items, item)
		}
	}
	includeStableDescendants := filter.IncludeNested || filter.IncludeDescendants
	for _, visible := range stableDelegateRowsForSession(s, includeStableDescendants) {
		item := projectStableDelegateListItem(now, visible)
		if stableJobListItemMatches(item, filter) {
			items = append(items, item)
		}
	}
	return items, nil
}

// turnSlotOccupancyOf snapshots the session's tree-counter occupancy for the
// job_list footer, or nil when nothing is held (the common idle case — no
// noise). Jobs/InUse/Cap describe the spawn budget; Drives reads the
// separate drive budget (driveCounter) so a drive-saturated tree with zero
// running jobs is visible rather than reporting a dead 0.
func turnSlotOccupancyOf(s *Session) *turnSlotOccupancy {
	if s == nil || s.treeCounter == nil {
		return nil
	}
	total, jobs, _, limit := s.treeCounter.occupancy()
	var drives int64
	if s.driveCounter != nil {
		drives, _, _, _ = s.driveCounter.occupancy()
	}
	if total == 0 && drives == 0 {
		return nil
	}
	return &turnSlotOccupancy{InUse: total, Cap: limit, Jobs: jobs, Drives: drives}
}

type stableDelegateVisibleRow struct {
	snapshot delegateSnapshot
	depth    int
}

func stableDelegateRowsForSession(s *Session, includeDescendants bool) []stableDelegateVisibleRow {
	if s == nil || s.delegateController == nil {
		return nil
	}
	snapshots := s.delegateController.Snapshot().rows
	byID := make(map[string]delegateSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.id] = snapshot
	}

	depths := make(map[string]int)
	for _, snapshot := range snapshots {
		if !delegateRowVisibleTo(s, snapshot.parentID, snapshot.descriptor.OwnerSessionID) {
			continue
		}
		depths[snapshot.id] = 0
	}
	if includeDescendants {
		for changed := true; changed; {
			changed = false
			for _, snapshot := range snapshots {
				if _, known := depths[snapshot.id]; known {
					continue
				}
				parentDepth, parentVisible := depths[snapshot.parentID]
				if !parentVisible {
					continue
				}
				if snapshot.descriptor.VisibleSessionID != "" && snapshot.descriptor.VisibleSessionID != s.id {
					continue
				}
				depths[snapshot.id] = parentDepth + 1
				changed = true
			}
		}
	}

	rows := make([]stableDelegateVisibleRow, 0, len(depths))
	for id, depth := range depths {
		rows = append(rows, stableDelegateVisibleRow{snapshot: byID[id], depth: depth})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].snapshot.id < rows[j].snapshot.id })
	return rows
}

func projectStableDelegateListItem(now time.Time, visible stableDelegateVisibleRow) jobListEntry {
	snapshot := visible.snapshot
	status := projectStableDelegateStatus(now, snapshot)
	resumable := snapshot.resumable
	item := jobListEntry{
		ID:                   snapshot.id,
		Kind:                 "delegate",
		Type:                 "delegate",
		Status:               status.Status,
		Description:          snapshot.descriptor.Description,
		OwnerSessionID:       snapshot.descriptor.OwnerSessionID,
		VisibleToSessionID:   snapshot.descriptor.VisibleSessionID,
		TranscriptRef:        stringPtrOrNil(snapshot.transcriptRef),
		Resumable:            &resumable,
		NotResumableReason:   stringPtrOrNil(snapshot.notResumableReason),
		StartedAt:            status.RunStartedAt,
		RunningForMS:         status.RunningForMS,
		DurationMS:           status.DurationMS,
		QuietForMS:           status.QuietForMS,
		LastActivity:         stringPtrOrNil(status.LatestActivityAt),
		Depth:                visible.depth,
		Task:                 snapshot.descriptor.Task,
		AgentType:            snapshot.descriptor.AgentType,
		Tools:                append([]string(nil), snapshot.descriptor.ToolNameCeiling...),
		Model:                snapshot.descriptor.ResolvedModel,
		ReasoningEffort:      snapshot.descriptor.Config.ReasoningEffort,
		ParentDelegateID:     stringPtrOrNil(snapshot.parentID),
		LatestActivitySortAt: snapshot.latestActivityAt,
	}
	if snapshot.lastOutcome != nil {
		item.Reason = stringPtrOrNil(snapshot.lastOutcome.Reason)
		item.ExhaustionBudget = string(snapshot.lastOutcome.ExhaustionBudget)
		item.ExhaustionLimit = snapshot.lastOutcome.ExhaustionLimit
		endedAt := snapshot.lastOutcome.EndedAt
		item.EndedAt = timePtrOrNil(&endedAt)
	}
	return item
}

func stableJobListItemMatches(item jobListEntry, filter listFilter) bool {
	status := jobstore.Status(item.Status)
	jobType := jobstore.JobType(item.Type)
	if filter.Status != "" && status != filter.Status {
		return false
	}
	if len(filter.Statuses) > 0 && !statusAllowed(status, filter.Statuses) {
		return false
	}
	if filter.Type != "" && jobType != filter.Type {
		return false
	}
	if len(filter.Types) > 0 && !typeAllowed(jobType, filter.Types) {
		return false
	}
	return true
}

func jobListItemActivity(item jobListEntry) time.Time {
	if !item.LatestActivitySortAt.IsZero() {
		return item.LatestActivitySortAt
	}
	for _, raw := range []*string{item.LastActivity, item.EndedAt} {
		if raw == nil || *raw == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, *raw); err == nil {
			return parsed
		}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, item.StartedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

// formatJobList renders job_list as plain text: a schema header, then one job per
// line — job_id, type, status, a label (description or shell command), and a
// bracketed detail tail (started time, reason, exit code, size, resumability) —
// then a count footer with the delegation allowance and any active/recent watches.
func formatJobList(out jobListResult) string {
	var b strings.Builder
	if len(out.Items) > 0 {
		b.WriteString("# id  type  status  label  [started · reason · exit · bytes]\n")
	}
	for _, j := range out.Items {
		fmt.Fprintf(&b, "%s  %s  %s", j.ID, j.Type, j.Status)
		if j.Depth > 0 {
			fmt.Fprintf(&b, "  depth=%d", j.Depth)
		}
		label := j.Description
		if label == "" && j.Command != nil {
			label = *j.Command
		}
		if label != "" {
			fmt.Fprintf(&b, "  %s", label)
		}
		var detail []string
		if started := shortTimestamp(j.StartedAt); started != "" {
			detail = append(detail, "started "+started)
		}
		if j.Reason != nil && *j.Reason != "" {
			detail = append(detail, *j.Reason)
		}
		if j.ExitCode != nil {
			detail = append(detail, fmt.Sprintf("exit %d", *j.ExitCode))
		}
		detail = append(detail, fmt.Sprintf("%d bytes", j.TotalBytes))
		// Surface resumability only when a job actually is resumable; resumable=false
		// for an ordinary shell job is noise (keeps rows lean).
		if j.Resumable != nil && *j.Resumable {
			detail = append(detail, "resumable")
		}
		fmt.Fprintf(&b, "  [%s]\n", strings.Join(detail, " · "))
	}
	if len(out.Items) == 0 {
		b.WriteString("No jobs.\n")
	}
	if out.Offset > 0 || out.Total > len(out.Items) {
		if len(out.Items) == 0 {
			// Offset past the end: never print an inverted "showing 51-50".
			fmt.Fprintf(&b, "\nshowing none of %d jobs (offset %d past end).", out.Total, out.Offset)
		} else {
			fmt.Fprintf(&b, "\nshowing %d-%d of %d jobs.", out.Offset+1, out.Offset+len(out.Items), out.Total)
		}
	} else {
		fmt.Fprintf(&b, "\n%d job(s).", out.Count)
	}
	if ts := out.TurnSlots; ts != nil {
		fmt.Fprintf(&b, " delegate turn slots: %d/%d in use (%d jobs, %d drive turns).", ts.InUse, ts.Cap, ts.Jobs, ts.Drives)
	}
	if out.DelegationAllowance > 0 {
		fmt.Fprintf(&b, " delegation_allowance: %d.", out.DelegationAllowance)
	}
	for _, w := range out.Watches {
		fmt.Fprintf(&b, "\nwatch %s → %s (%s)", w.ID, w.Source, w.Condition)
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "\nrecent watch %s → %s (%s, %d delivered)", w.ID, w.Source, w.EndReason, w.Deliveries)
	}
	return b.String()
}

// shortTimestamp renders an RFC3339Nano timestamp as "YYYY-MM-DD HH:MM" for
// compact display, returning "" if it cannot be parsed.
func shortTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func jobStopTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "target"))
	if target == "" {
		return "", errors.New("invalid_request: target is required")
	}
	// max_wait_ms: 0/absent = request stop and return; positive = wait up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	maxWaitMS := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		maxWaitMS = n
	}
	if strings.HasPrefix(target, "dlg_") {
		return stopStableDelegate(ctx, s, target, maxWaitMS)
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}

	targetJM := jm
	if routed, rec, err := s.nestedOrLocalJobManager(target); err == nil {
		if string(rec.Type) == delegateResourceType {
			return "", fmt.Errorf("legacy_delegate_activation: %s is a retired delegate activation alias; use its stable delegate_id", target)
		}
		targetJM = routed
	}
	var childStopErr error
	if shellBoolArg(args, "include_children") {
		_, childStopErr = s.stopChildren(target)
	}
	var previousStatus jobstore.Status
	if _, pre, lookupErr := s.nestedOrLocalJobManager(target); lookupErr == nil && pre != nil {
		previousStatus = pre.Status
	}
	rec, err := stopNestedOrLocalForJobStop(s, target)
	if err != nil {
		return "", errors.Join(childStopErr, err)
	}
	if maxWaitMS > 0 {
		done := waitForJobDone(ctx, targetJM, target, clampJobBlockTimeout(maxWaitMS))
		if _, latest, err := s.nestedOrLocalJobManager(target); err == nil {
			rec = latest
		}
		if !done && rec != nil && !rec.Status.IsTerminal() {
			pending := cloneJobRecord(rec)
			pending.Reason = "stop_pending"
			rec = pending
		}
	}
	if childStopErr != nil {
		return "", childStopErr
	}
	_ = maxChars
	stop := jobStopResult{
		ID:             rec.JobID,
		JobID:          rec.JobID,
		Type:           string(rec.Type),
		Status:         string(rec.Status),
		Reason:         stringPtrOrNil(rec.Reason),
		PreviousStatus: string(previousStatus),
		Outcome:        classifyStopOutcome(previousStatus, rec),
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

func stopStableDelegate(ctx context.Context, s *Session, delegateID string, maxWaitMS int) (any, error) {
	if s == nil || s.delegateController == nil {
		return "", errors.New("delegate controller is unavailable")
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return "", err
	}
	result, cancelPlan, plans, err := s.delegateController.StopSubtreeAndDrive(actor, delegateID)
	if err != nil {
		return "", err
	}
	// #655: report the watches that survive this stop and keep delivering to
	// the stopped delegate subtree. One read, taken at result time — after the
	// wait/timeout decision — so the reported set is the current one on every
	// path (settled, timed out, or request-and-return). The inventory covers
	// the subtree, not just the target, because a subtree stop leaves
	// descendant watches live too.
	executeDelegateCancelPlan(cancelPlan)
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		return "", err
	}
	completed := false
	if maxWaitMS > 0 {
		completed = waitForDelegateStopDone(ctx, s, result.done, clampJobBlockTimeout(maxWaitMS))
	}
	live := s.liveWatchesDeliveringToDelegate(delegateID)
	stop := stableDelegateStopResult(result, completed, actor.describe())
	stop.LiveWatches = live
	if completed {
		populateDelegateStopEvidence(&stop, s, delegateID)
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

// populateDelegateStopEvidence fills in the cancellation provenance kata tpb0
// asks for once a stop has actually completed: whether this exact delegate
// resource can still be resumed, and whatever partial scratch/worktree
// evidence its run loop had already gathered (preserved through FinishGeneration's
// PhaseStopping branch, see delegate_tree_finish.go). Reuses the same snapshot
// row job_status and job_list already project — no new read path.
func populateDelegateStopEvidence(stop *jobStopResult, s *Session, delegateID string) {
	for _, row := range stableDelegateRowsForSession(s, true) {
		if row.snapshot.id != delegateID {
			continue
		}
		resumable := row.snapshot.resumable
		stop.Resumable = &resumable
		stop.NotResumableReason = row.snapshot.notResumableReason
		if packet := row.snapshot.latestPacket; packet != nil && len(packet.Metadata) != 0 {
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err == nil {
				stop.ScratchPath = metadata.ScratchPath
				if wt := metadata.Worktree; wt != nil {
					stop.Worktree = &delegateWorktreeToolResult{
						Path: wt.Path, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Ahead: wt.Ahead, Dirty: wt.Dirty,
					}
				}
			}
		}
		return
	}
}

func waitForDelegateStopDone(ctx context.Context, s *Session, done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	default:
	}
	timer := s.sclock().NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C():
		return false
	case <-ctx.Done():
		return false
	}
}

func stableDelegateStopResult(result delegateStopResult, completed bool, requestedBy string) jobStopResult {
	reason := "stop_pending"
	status := string(jobstore.StatusRunning)
	outcome := "stop_requested"
	if completed {
		reason = "stopped_by_parent"
		status = string(result.lifecycle)
		outcome = result.outcome
	}
	return jobStopResult{
		ID:             result.id,
		JobID:          result.id,
		Type:           "delegate",
		Status:         status,
		Reason:         &reason,
		PreviousStatus: string(result.previousLifecycle),
		Outcome:        outcome,
		RequestedBy:    requestedBy,
	}
}

var stopNestedOrLocalForJobStop = func(s *Session, jobID string) (*jobstore.JobRecord, error) {
	return s.stopNestedOrLocal(jobID)
}

type jobStatusResult struct {
	JobID            string  `json:"job_id"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	Description      string  `json:"description"`
	Phase            string  `json:"phase,omitempty"`
	Reason           *string `json:"reason,omitempty"`
	ExhaustionBudget string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int     `json:"exhaustion_limit,omitempty"`
	Resumable        *bool   `json:"resumable,omitempty"`
	RunningForMS     *int64  `json:"running_for_ms,omitempty"`
	DurationMS       *int64  `json:"duration_ms,omitempty"`
	QuietForMS       *int64  `json:"quiet_for_ms,omitempty"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	LastEventAt      *string `json:"last_event_at,omitempty"`
	TranscriptRef    string  `json:"transcript_ref"`
	ExitCode         *int    `json:"exit_code,omitempty"`
}

type stableDelegateStatusResult struct {
	ID                 string                 `json:"id"`
	Type               string                 `json:"type"`
	Status             string                 `json:"status"`
	Task               string                 `json:"task"`
	Description        string                 `json:"description,omitempty"`
	AgentType          string                 `json:"agent_type"`
	Tools              []string               `json:"tools,omitempty"`
	Model              string                 `json:"model,omitempty"`
	ReasoningEffort    string                 `json:"reasoning_effort,omitempty"`
	Resumable          bool                   `json:"resumable"`
	NeedsAttention     bool                   `json:"needs_attention"`
	NotResumableReason string                 `json:"not_resumable_reason,omitempty"`
	TranscriptRef      string                 `json:"transcript_ref"`
	RunStartedAt       string                 `json:"run_started_at,omitempty"`
	LatestActivityAt   string                 `json:"latest_activity_at,omitempty"`
	RunningForMS       *int64                 `json:"running_for_ms,omitempty"`
	QuietForMS         *int64                 `json:"quiet_for_ms,omitempty"`
	DurationMS         *int64                 `json:"duration_ms,omitempty"`
	LastOutcome        *delegatestore.Outcome `json:"last_outcome,omitempty"`
}

func projectStableDelegateStatus(now time.Time, snapshot delegateSnapshot) stableDelegateStatusResult {
	descriptor := snapshot.descriptor
	out := stableDelegateStatusResult{
		ID:                 snapshot.id,
		Type:               "delegate",
		Status:             string(snapshot.lifecycle),
		Task:               descriptor.Task,
		Description:        descriptor.Description,
		AgentType:          descriptor.AgentType,
		Tools:              append([]string(nil), descriptor.ToolNameCeiling...),
		Model:              descriptor.ResolvedModel,
		ReasoningEffort:    descriptor.Config.ReasoningEffort,
		Resumable:          snapshot.resumable,
		NeedsAttention:     snapshot.needsAttention,
		NotResumableReason: snapshot.notResumableReason,
		TranscriptRef:      snapshot.transcriptRef,
		LastOutcome:        snapshot.lastOutcome,
	}
	if !snapshot.runStartedAt.IsZero() {
		out.RunStartedAt = snapshot.runStartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !snapshot.latestActivityAt.IsZero() {
		out.LatestActivityAt = snapshot.latestActivityAt.UTC().Format(time.RFC3339Nano)
	}
	if snapshot.currentRunOpen && !snapshot.runStartedAt.IsZero() {
		running := max(now.Sub(snapshot.runStartedAt).Milliseconds(), int64(0))
		out.RunningForMS = &running
		quietSince := snapshot.latestActivityAt
		if quietSince.IsZero() {
			quietSince = snapshot.runStartedAt
		}
		quiet := max(now.Sub(quietSince).Milliseconds(), int64(0))
		out.QuietForMS = &quiet
	} else if snapshot.lastOutcome != nil && !snapshot.runStartedAt.IsZero() && !snapshot.lastOutcome.EndedAt.IsZero() {
		duration := max(snapshot.lastOutcome.EndedAt.Sub(snapshot.runStartedAt).Milliseconds(), int64(0))
		out.DurationMS = &duration
	}
	return out
}

// TurnSlotOccupancy is the diagnostic tree-counter snapshot surfaced in
// job_list while any delegate-turn slot is held: spawn-budget total in use,
// cap, and jobs, plus drive turns in flight on the separate drive budget.
type TurnSlotOccupancy struct {
	InUse int64 `json:"in_use"`
	Cap   int64 `json:"cap"`
	Jobs  int64 `json:"jobs"`
	// Drives is the live occupancy of the separate drive-turn budget
	// (driveCounter); drive turns do not hold spawn-budget slots.
	Drives int64 `json:"drive_turns"`
}

type turnSlotOccupancy = TurnSlotOccupancy

type jobListResult struct {
	Items     []jobListEntry     `json:"items"`
	Jobs      []jobListEntry     `json:"-"`
	Count     int                `json:"count"`
	Offset    int                `json:"offset,omitempty"`
	Total     int                `json:"total"`
	TurnSlots *turnSlotOccupancy `json:"turn_slots,omitempty"`
	// Watches/RecentWatches/DelegationAllowance are supervision signal kept only
	// when they carry information: no active watches, no recent watch history, and
	// a no-op delegation allowance (≤ 1, which can only grant 0) are all omitted.
	Watches             []watchListEntry   `json:"watches,omitempty"`
	RecentWatches       []recentWatchEntry `json:"recent_watches,omitempty"`
	DelegationAllowance int                `json:"delegation_allowance,omitempty"`
}

// watchListEntry is one active watch in job_list's result, projected from the
// session's live watch configs. condition is a compact one-line summary of the
// watch's trigger; created_at is the watch's install time as an RFC3339Nano
// timestamp.
type watchListEntry struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	CreatedAt  string `json:"created_at"`
}

// recentWatchEntry is one watch that has left the active set, surfaced by job_list's
// bounded recent_watches ring so a fired-then-removed watch stays legible. end_reason
// is one of auto_removed_terminal, cleared, replaced, budget_exhausted.
type recentWatchEntry struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	EndReason  string `json:"end_reason"`
	EndedAt    string `json:"ended_at"`
}

type jobListEntry struct {
	ID               string   `json:"id"`
	JobID            string   `json:"job_id,omitempty"`
	Kind             string   `json:"kind"`
	Type             string   `json:"type"`
	Status           string   `json:"status"`
	Phase            string   `json:"phase,omitempty"`
	Reason           *string  `json:"reason,omitempty"`
	Description      string   `json:"description"`
	Task             string   `json:"task,omitempty"`
	AgentType        string   `json:"agent_type,omitempty"`
	Tools            []string `json:"tools,omitempty"`
	Model            string   `json:"model,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ParentJobID      *string  `json:"parent_job_id,omitempty"`
	ParentDelegateID *string  `json:"parent_delegate_id,omitempty"`
	OwnerSessionID   string   `json:"owner_session_id"`
	// VisibleToSessionID is internal visibility routing — in a plain list it always
	// equals the owner, so it is kept for tooling but omitted from the model wire.
	VisibleToSessionID string `json:"-"`
	// TranscriptRef/Resumable/NotResumableReason are omitempty: nil for the common
	// running-shell scan (so they vanish), present where they carry signal (a
	// delegate's transcript handle, a runtime-lost delegate's resumability).
	TranscriptRef      *string `json:"transcript_ref,omitempty"`
	ExhaustionBudget   string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit    int     `json:"exhaustion_limit,omitempty"`
	Resumable          *bool   `json:"resumable,omitempty"`
	NotResumableReason *string `json:"not_resumable_reason,omitempty"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at,omitempty"`
	RunningForMS       *int64  `json:"running_for_ms,omitempty"`
	DurationMS         *int64  `json:"duration_ms,omitempty"`
	QuietForMS         *int64  `json:"quiet_for_ms,omitempty"`
	LastEventAt        *string `json:"last_event_at,omitempty"`
	// LastActivity is the most recent parent-observable activity timestamp
	// (output append or start) for a running job; for a terminal record with no
	// live stamp it falls back to ended_at, then started_at. A quiet running
	// delegate stays at its started_at, which is exactly what the quiet-job
	// watchdog surfaces. current_action is intentionally omitted: a running
	// delegate's mid-run "current action" is not cheaply readable from
	// parent-side state without cross-session probing.
	LastActivity *string `json:"last_activity"`
	ExitCode     *int    `json:"exit_code,omitempty"`
	// TotalBytes is the lifetime output byte count — the same name and concept the
	// shell result and job transcript report, so the field is consistent across
	// every tool the agent reads.
	TotalBytes int64 `json:"total_bytes"`
	// Command is the shell command line for a shell job, omitted for delegates (which
	// have none) so the row stays lean. It lets the agent identify a job without
	// reading the transcript when the description is sparse.
	Command *string `json:"command,omitempty"`
	// Depth is the live-subtree distance from the calling session, populated only
	// by job_list(include_descendants=true): 0 for the caller's own store, 1 for
	// a direct child's store, and so on. It is the depth of the store the row was
	// surfaced from, so a dead descendant's terminal forwarded copy that survives
	// in an ancestor store carries that ancestor's depth. Omitted when 0 for the
	// default and include_nested listings, which do not walk the tree.
	Depth int `json:"depth,omitempty"`

	LatestActivitySortAt time.Time `json:"-"`
}

type jobStopResult struct {
	ID             string  `json:"id"`
	JobID          string  `json:"-"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	PreviousStatus string  `json:"previous_status"`
	Outcome        string  `json:"outcome"`
	// RequestedBy, Resumable, NotResumableReason, ScratchPath, and Worktree are
	// delegate-only cancellation provenance (kata tpb0): who requested the
	// stop, whether the same delegate resource can still be resumed, and
	// whatever partial evidence its run loop had already gathered. All are
	// empty/nil for a shell job_stop. RequestedBy is known at admission and so
	// is reported on every delegate stop; the rest are read from the settled
	// delegate row and stay empty/nil until the stop completes (status/outcome
	// still "running"/"stop_requested").
	RequestedBy        string                      `json:"requested_by,omitempty"`
	Resumable          *bool                       `json:"resumable,omitempty"`
	NotResumableReason string                      `json:"not_resumable_reason,omitempty"`
	ScratchPath        string                      `json:"scratch_path,omitempty"`
	Worktree           *delegateWorktreeToolResult `json:"worktree,omitempty"`
	// LiveWatches is the #655 provenance: watches that survive this stop and
	// keep delivering to the stopped delegate (receiver-keyed). Reported on
	// every delegate stop — admission-time as well as settle-time — because
	// the parent otherwise has no way to learn they exist. Empty for a shell
	// job_stop or a delegate with no surviving watches.
	LiveWatches []watchListEntry `json:"live_watches,omitempty"`
}

// formatJobStop renders a job_stop result as a single plain-text line matching the
// job-family footer style: [job <id> · <status> · <outcome> · <reason>]. For a
// completed delegate stop it appends the cancellation provenance kata tpb0
// asks for: the requesting actor, the delegate's resumable classification,
// and any partial scratch/worktree evidence its run loop had already
// gathered before being cancelled.
func formatJobStop(out jobStopResult) string {
	id := out.ID
	if id == "" {
		id = out.JobID
	}
	parts := []string{out.Type + " " + id, out.Status, out.Outcome}
	if out.Reason != nil && *out.Reason != "" {
		parts = append(parts, *out.Reason)
	}
	if out.Type == "delegate" && out.PreviousStatus != "" {
		parts = append(parts, "was "+out.PreviousStatus)
	}
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(strings.Join(parts, " · "))
	b.WriteString("]")
	if out.Type != "delegate" {
		return b.String()
	}
	if out.RequestedBy != "" {
		fmt.Fprintf(&b, "\nrequested by: %s", out.RequestedBy)
	}
	if out.Resumable != nil {
		if *out.Resumable {
			b.WriteString("\nresumable: yes")
		} else {
			reason := out.NotResumableReason
			if reason == "" {
				reason = "not resumable"
			}
			fmt.Fprintf(&b, "\nresumable: no (%s)", reason)
		}
	}
	if out.ScratchPath != "" {
		fmt.Fprintf(&b, "\nscratch: %s", out.ScratchPath)
	}
	if out.Worktree != nil {
		fmt.Fprintf(&b, "\nworktree: path=%s, branch=%s, head=%s, %d commits ahead, dirty=%t",
			out.Worktree.Path, out.Worktree.Branch, out.Worktree.HeadSHA, out.Worktree.Ahead, out.Worktree.Dirty)
	}
	if n := len(out.LiveWatches); n > 0 {
		fmt.Fprintf(&b, "\nlive watches: %d still armed and delivering to this delegate", n)
		for i, row := range out.LiveWatches {
			if i >= 5 {
				fmt.Fprintf(&b, "\n  +%d more (see live_watches in state)", n-i)
				break
			}
			detail := row.Condition
			if detail == "" {
				detail = "no condition"
			}
			fmt.Fprintf(&b, "\n  %s · source=%s · %s · clear it (job_watch operation=\"clear\" watch_id=%s)", row.ID, row.Source, detail, row.ID)
		}
	}
	return b.String()
}

// classifyStopOutcome distinguishes a stop that cancelled a live job from one that
// raced with, or arrived after, the job's own completion. previous is the job's
// status read before the stop signal; rec is the record after it.
func classifyStopOutcome(previous jobstore.Status, rec *jobstore.JobRecord) string {
	if previous.IsTerminal() {
		return "already_terminal"
	}
	if rec == nil || !rec.Status.IsTerminal() {
		return "stop_requested" // still finalizing (e.g. reason "stop_pending")
	}
	if rec.Status == jobstore.StatusCancelled {
		return "cancelled_by_request"
	}
	return "completed_during_stop"
}

type delegateSendResult struct {
	DelegateID             string                  `json:"delegate_id,omitempty"`
	Type                   string                  `json:"type,omitempty"`
	Status                 string                  `json:"status,omitempty"`
	Reason                 *string                 `json:"reason,omitempty"`
	ExhaustionBudget       string                  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit        int                     `json:"exhaustion_limit,omitempty"`
	Resumable              *bool                   `json:"resumable,omitempty"`
	RunningInBackground    bool                    `json:"running_in_background"`
	TimedOut               bool                    `json:"timed_out,omitempty"`
	Action                 string                  `json:"action"`
	TranscriptRef          string                  `json:"transcript_ref,omitempty"`
	Output                 *string                 `json:"output,omitempty"`
	Truncated              *bool                   `json:"truncated,omitempty"`
	StructuredResult       any                     `json:"structured_result,omitempty"`
	StructuredResultValid  *bool                   `json:"structured_result_valid,omitempty"`
	StructuredResultReason string                  `json:"structured_result_reason,omitempty"`
	Warnings               []string                `json:"warnings,omitempty"`
	Task                   string                  `json:"task,omitempty"`
	Description            string                  `json:"description,omitempty"`
	AgentType              string                  `json:"agent_type,omitempty"`
	Tools                  []string                `json:"tools,omitempty"`
	RequestedModel         string                  `json:"requested_model,omitempty"`
	ResolvedProfileID      string                  `json:"resolved_profile_id,omitempty"`
	ResolvedModel          string                  `json:"resolved_model,omitempty"`
	ReasoningEffort        string                  `json:"reasoning_effort,omitempty"`
	RunStartedAt           string                  `json:"run_started_at,omitempty"`
	RunEndedAt             string                  `json:"run_ended_at,omitempty"`
	LatestActivityAt       string                  `json:"latest_activity_at,omitempty"`
	CumulativeUsage        *schema.CumulativeUsage `json:"cumulative_usage,omitempty"`
	// Worktree carries the isolation lane's path/branch/ahead/dirty state for
	// an isolated delegate's terminal job (native worktree tools spec §9
	// lifecycle step 3); nil for a non-isolated delegate.
	Worktree          *delegateWorktreeToolResult `json:"worktree,omitempty"`
	WaitIgnoredReason string                      `json:"wait_ignored_reason,omitempty"`
}

type jobWatchToolResult struct {
	WatchID            string                   `json:"watch_id,omitempty"`
	Source             string                   `json:"source"`
	Watching           bool                     `json:"watching"`
	OutputMatch        string                   `json:"output_match,omitempty"`
	Events             []string                 `json:"events,omitempty"`
	EventFilter        *jobWatchToolEventFilter `json:"event_filter,omitempty"`
	ProgressIntervalMS int                      `json:"progress_interval_ms,omitempty"`
	Send               *jobWatchToolSendArgs    `json:"send,omitempty"`
	// replaced_existing and fired serialize explicitly even when false: the
	// contract's install example shows replaced_existing:false, and §7.1
	// promises "fired=false on none" for terminal catch-up.
	ReplacedExisting bool   `json:"replaced_existing"`
	Fired            bool   `json:"fired"`
	TerminalCatchup  bool   `json:"terminal_catchup,omitempty"`
	Status           string `json:"status,omitempty"`
}

type jobWatchToolEventFilter struct {
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
}

type jobWatchListToolResult struct {
	Watches       []jobWatchInspectToolResult `json:"watches"`
	RecentWatches []jobWatchInspectToolResult `json:"recent_watches,omitempty"`
	Count         int                         `json:"count"`
}

type jobWatchInspectToolResult struct {
	WatchID    string `json:"watch_id"`
	Source     string `json:"source,omitempty"`
	Watching   bool   `json:"watching"`
	Condition  string `json:"condition,omitempty"`
	Deliveries int    `json:"deliveries,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	EndReason  string `json:"end_reason,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

type jobWatchToolSendArgs struct {
	To             string `json:"to"`
	Message        string `json:"message,omitempty"`
	IncludeExcerpt bool   `json:"include_excerpt,omitempty"`
}

// delegateWorktreeToolResult is the tool-facing shape of delegateWorktreeReport
// (native worktree tools spec §9 lifecycle step 3).
type delegateWorktreeToolResult struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	Ahead   int    `json:"ahead_commits"`
	Dirty   bool   `json:"dirty"`
	// DisposalHint carries the spec §P2 completion nudge. It MUST be exported —
	// an unexported field is silently dropped by encoding/json (roborev finding
	// 2718-2). Empty (and omitted) unless the receiving session has the dispose
	// op and owns the delegate.
	DisposalHint string `json:"disposal_hint,omitempty"`
}

func delegateWorktreeToolResultFrom(wt *delegateWorktreeReport) *delegateWorktreeToolResult {
	if wt == nil {
		return nil
	}
	return &delegateWorktreeToolResult{Path: wt.Path, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Ahead: wt.Ahead, Dirty: wt.Dirty, DisposalHint: wt.DisposalHint}
}

type delegateSandboxToolResult struct {
	Mode    string `json:"mode"`
	Network bool   `json:"network"`
}

func delegateSandboxToolResultFrom(sb *delegateSandboxReport) *delegateSandboxToolResult {
	if sb == nil {
		return nil
	}
	return &delegateSandboxToolResult{Mode: sb.Mode, Network: sb.Network}
}

func marshalDelegateSendResult(res sendMessageResult, maxChars int) (any, error) {
	_ = maxChars
	out := delegateSendResult{
		DelegateID:          res.DelegateID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		ExhaustionBudget:    res.ExhaustionBudget,
		ExhaustionLimit:     res.ExhaustionLimit,
		Resumable:           res.Resumable,
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		Action:              res.Action,
		TranscriptRef:       res.TranscriptRef,
		Warnings:            append([]string(nil), res.Warnings...),
		Task:                res.Task,
		Description:         res.Description,
		AgentType:           res.AgentType,
		Tools:               append([]string(nil), res.Tools...),
		RequestedModel:      res.RequestedModel,
		ResolvedProfileID:   res.ResolvedProfileID,
		ResolvedModel:       res.ResolvedModel,
		ReasoningEffort:     res.ReasoningEffort,
		RunStartedAt:        res.RunStartedAt,
		RunEndedAt:          res.RunEndedAt,
		LatestActivityAt:    res.LatestActivityAt,
		CumulativeUsage:     res.CumulativeUsage,
		Worktree:            delegateWorktreeToolResultFrom(res.Worktree),
		WaitIgnoredReason:   res.WaitIgnoredReason,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
		out.StructuredResultReason = res.StructuredResultReason
	}
	return tool.StateResult{Output: formatDelegateSend(out), State: out}, nil
}

// formatDelegateSend renders a delegate send/steer/start result: any reply output,
// a bracketed footer, and the structured_result (JSON, genuinely structured) when
// present.
func formatDelegateSend(out delegateSendResult) string {
	var b strings.Builder
	if out.Output != nil && *out.Output != "" {
		b.WriteString(*out.Output)
		if !strings.HasSuffix(*out.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	foot := []string{out.Action}
	if out.DelegateID != "" {
		foot = append([]string{"delegate_id " + out.DelegateID}, foot...)
	}
	if out.Status != "" {
		foot = append(foot, out.Status)
	}
	if out.RunningInBackground {
		foot = append(foot, "running in background")
	}
	if out.WaitIgnoredReason != "" {
		foot = append(foot, "wait ignored: "+out.WaitIgnoredReason)
	}
	b.WriteString("[")
	b.WriteString(strings.Join(foot, " · "))
	b.WriteString("]")
	if out.Worktree != nil {
		fmt.Fprintf(&b, "\nworktree: path=%s, branch=%s, head=%s, %d commits ahead, dirty=%t",
			out.Worktree.Path, out.Worktree.Branch, out.Worktree.HeadSHA,
			out.Worktree.Ahead, out.Worktree.Dirty)
	}
	for _, warning := range out.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", warning)
	}
	if out.StructuredResult != nil {
		if sr, err := json.Marshal(out.StructuredResult); err == nil {
			valid := out.StructuredResultValid != nil && *out.StructuredResultValid
			fmt.Fprintf(&b, "\nstructured_result (valid=%v): %s", valid, sr)
		}
	}
	if out.Worktree != nil && out.Worktree.DisposalHint != "" {
		fmt.Fprintf(&b, "\ndisposal_hint: %s", out.Worktree.DisposalHint)
	}
	return b.String()
}

func marshalWatchResult(res watchResult, maxChars int) (any, error) {
	_ = maxChars
	out := jobWatchToolResult{
		WatchID:            res.WatchID,
		Source:             watchPublicSource(res.Source, res.Target),
		Watching:           res.Watching,
		OutputMatch:        res.OutputMatch,
		Events:             res.Events,
		ProgressIntervalMS: res.ProgressIntervalMS,
		ReplacedExisting:   res.ReplacedExisting,
		Fired:              res.Fired,
		TerminalCatchup:    res.TerminalCatchup,
		Status:             res.Status,
	}
	if res.EventFilter != nil {
		out.EventFilter = &jobWatchToolEventFilter{
			ToolName: res.EventFilter.ToolName,
			Status:   res.EventFilter.Status,
		}
	}
	if res.Send != nil {
		out.Send = &jobWatchToolSendArgs{
			To:             res.Send.To,
			Message:        res.Send.Message,
			IncludeExcerpt: res.Send.IncludeExcerpt,
		}
	}
	return tool.StateResult{Output: formatJobWatch(out), State: out}, nil
}

func marshalWatchListResult(res jobWatchListToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchList(res), State: res}, nil
}

func marshalWatchInspectResult(res jobWatchInspectToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchInspect(res), State: res}, nil
}

// formatJobWatch renders a job_watch result as a one-line footer summarizing the
// watch's source, trigger condition, and disposition.
func formatJobWatch(out jobWatchToolResult) string {
	if out.TerminalCatchup {
		parts := []string{"watch on " + out.Source, "terminal catch-up"}
		if out.Fired {
			parts = append(parts, "fired")
		} else {
			parts = append(parts, "not fired")
		}
		if out.Status != "" {
			parts = append(parts, out.Status)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	if !out.Watching {
		if out.Source == "" && out.WatchID != "" {
			return fmt.Sprintf("[watch_id %s cleared]", out.WatchID)
		}
		parts := []string{"watch on " + out.Source + " cleared"}
		if out.WatchID != "" {
			parts = append(parts, "watch_id "+out.WatchID)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	parts := []string{"watching " + out.Source}
	if out.WatchID != "" {
		parts = append(parts, "watch_id "+out.WatchID)
	}
	var cond []string
	if out.OutputMatch != "" {
		cond = append(cond, "output_match: "+out.OutputMatch)
	}
	if len(out.Events) > 0 {
		events := "events: " + strings.Join(out.Events, ",")
		if filter := formatJobWatchEventFilter(out.EventFilter); filter != "" {
			events += " " + filter
		}
		cond = append(cond, events)
	}
	if out.ProgressIntervalMS > 0 {
		cond = append(cond, fmt.Sprintf("every %dms", out.ProgressIntervalMS))
	}
	if len(cond) > 0 {
		parts = append(parts, strings.Join(cond, " "))
	}
	if out.Send != nil && out.Send.To != "" {
		parts = append(parts, "→ "+out.Send.To)
	}
	if out.ReplacedExisting {
		parts = append(parts, "replaced existing")
	}
	if out.Fired {
		parts = append(parts, "fired")
	}
	if out.Status != "" {
		parts = append(parts, out.Status)
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

func formatJobWatchEventFilter(filter *jobWatchToolEventFilter) string {
	if filter == nil {
		return ""
	}
	var parts []string
	if filter.ToolName != "" {
		parts = append(parts, "tool_name="+filter.ToolName)
	}
	if filter.Status != "" {
		parts = append(parts, "status="+filter.Status)
	}
	if len(parts) == 0 {
		return ""
	}
	return "where " + strings.Join(parts, ",")
}

func formatJobWatchList(out jobWatchListToolResult) string {
	if len(out.Watches) == 0 && len(out.RecentWatches) == 0 {
		return "no watches"
	}
	var b strings.Builder
	for _, w := range out.Watches {
		status := "watching"
		if !w.Watching {
			status = "pending"
		}
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, status, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, w.EndReason, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatJobWatchInspect(out jobWatchInspectToolResult) string {
	if out.Watching {
		parts := []string{"watching " + out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	if out.EndReason != "" {
		return fmt.Sprintf("%s  %s  %s", out.WatchID, out.EndReason, out.Source)
	}
	if out.Source != "" {
		parts := []string{"pending", out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	return out.WatchID + "  not found"
}

func sessionJobManager(s *Session) (*jobManager, error) {
	if s == nil || s.jobManager == nil {
		return nil, errors.New(jobManagerUnavailableReason)
	}
	return s.jobManager, nil
}

// sessionRunningJobIDs lists this session's own running (session-launched,
// non-nested) job ids, in job_list's default order. Used by the communicate
// end_turn running-job warning; returns nil when the session has no job
// manager or no running jobs.
func sessionRunningJobIDs(s *Session) []string {
	jm, err := sessionJobManager(s)
	if err != nil {
		return nil
	}
	recs := jm.list(listFilter{Status: jobstore.StatusRunning})
	if len(recs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.JobID)
	}
	return ids
}

// sessionRunningWorkIDs combines managed jobs with detached processes owned by
// this session. Detached processes deliberately stay out of the job manager and
// drain accounting, but an end_turn warning must still name one while its own
// completion receipt is open.
func sessionRunningWorkIDs(s *Session) []string {
	ids := sessionRunningJobIDs(s)
	return append(ids, s.runningDetachedProcessIDs()...)
}

func (s *Session) recordDetachedProcess(process execenv.DetachedProcess) {
	if s == nil || process.PID <= 0 || process.Done == nil {
		// Without a completion receipt we cannot distinguish a live process from a
		// stale PID safely, so do not manufacture an end-turn warning.
		return
	}
	s.mu.Lock()
	s.detachedProcesses = append(s.detachedProcesses, sessionDetachedProcess{
		pid:  process.PID,
		done: process.Done,
	})
	s.mu.Unlock()
}

// runningDetachedProcessIDs returns only processes whose launcher's completion
// receipt is still open. The receipt belongs to this exact launch, so a later
// process reusing the same PID can never be mistaken for a live owned process.
func (s *Session) runningDetachedProcessIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	alive := s.detachedProcesses[:0]
	ids := make([]string, 0, len(s.detachedProcesses))
	for _, process := range s.detachedProcesses {
		select {
		case <-process.done:
			continue
		default:
			alive = append(alive, process)
			ids = append(ids, fmt.Sprintf("detached process (pid %d)", process.pid))
		}
	}
	s.detachedProcesses = alive
	return ids
}

func jobListFilterFromArgs(args map[string]any) (listFilter, error) {
	limit := defaultJobListLimit
	if n, ok := shellIntArg(args, "limit"); ok {
		limit = n
	}
	if limit <= 0 {
		return listFilter{}, errors.New("limit must be greater than 0")
	}
	if limit > maxJobListLimit {
		limit = maxJobListLimit
	}
	offset := 0
	if n, ok := shellIntArg(args, "offset"); ok {
		offset = n
	}
	if offset < 0 {
		return listFilter{}, errors.New("offset must be non-negative")
	}

	statuses, err := jobStatusArrayArg(args, "status")
	if err != nil {
		return listFilter{}, err
	}
	types, err := jobTypeArrayArg(args, "type")
	if err != nil {
		return listFilter{}, err
	}
	return listFilter{
		Statuses:           statuses,
		Types:              types,
		Limit:              limit,
		Offset:             offset,
		IncludeNested:      shellBoolArg(args, "include_nested"),
		IncludeDescendants: shellBoolArg(args, "include_descendants"),
	}, nil
}

func jobStatusArrayArg(args map[string]any, key string) ([]jobstore.Status, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	statuses := make([]jobstore.Status, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", key)
		}
		status := jobstore.Status(s)
		switch status {
		case jobstore.StatusRunning,
			jobstore.Status("idle"), jobstore.Status("settling"), jobstore.Status("stopping"), jobstore.Status("closed"),
			jobstore.StatusCompleted, jobstore.StatusFailed, jobstore.StatusExhausted, jobstore.StatusCancelled, jobstore.StatusStopped:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("invalid job status %q", status)
		}
	}
	return statuses, nil
}

func watchArgsFromToolArgs(args map[string]any) (watchArgs, error) {
	operation := strings.TrimSpace(stringArg(args, "operation"))
	if operation == "" {
		return watchArgs{}, errors.New("invalid_request: operation is required")
	}
	if _, ok := args["target"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch uses source, not target")
	}
	if _, ok := args["send"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch delivers to the watcher automatically; send is not a public argument")
	}
	if _, ok := args["receiver_session_id"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch derives its receiver from the watcher session")
	}
	if _, ok := args["receiver_delegate_id"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch derives its receiver from the watcher session")
	}
	a := watchArgs{
		Operation:   operation,
		WatchID:     strings.TrimSpace(stringArg(args, "watch_id")),
		Source:      strings.TrimSpace(stringArg(args, "source")),
		OutputMatch: stringArg(args, "output_match"),
	}
	if n, ok := shellIntArg(args, "progress_interval_ms"); ok {
		a.ProgressIntervalMS = n
	}
	events, err := stringArrayArg(args, "events")
	if err != nil {
		return watchArgs{}, err
	}
	a.Events = events
	if n, ok := shellIntArg(args, "every"); ok {
		a.Every = n
	}
	eventFilter, err := watchEventFilterArg(args)
	if err != nil {
		return watchArgs{}, err
	}
	a.EventFilter = eventFilter
	switch a.Operation {
	case "create":
		if a.Source == "" {
			return watchArgs{}, errors.New("invalid_request: source is required")
		}
	case "list":
		if a.Source != "" || a.WatchID != "" {
			return watchArgs{}, errors.New("invalid_request: list requires no source or watch_id")
		}
	case "inspect", "clear":
		if a.WatchID == "" {
			return watchArgs{}, errors.New("invalid_request: watch_id is required")
		}
	default:
		return watchArgs{}, fmt.Errorf("invalid_request: unsupported operation %q", a.Operation)
	}
	if a.Source == "*" {
		return watchArgs{}, errors.New("invalid_request: wildcard watch target is not supported in v1")
	}
	return a, nil
}

func watchEventFilterArg(args map[string]any) (*watchEventFilter, error) {
	raw, ok := args["event_filter"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("event_filter must be an object")
	}
	var filter watchEventFilter
	for key, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid_request: event_filter.%s must be a string", key)
		}
		switch key {
		case "tool_name":
			filter.ToolName = strings.TrimSpace(s)
		case "status":
			filter.Status = strings.TrimSpace(s)
		default:
			return nil, fmt.Errorf("invalid_request: unknown event_filter field %q", key)
		}
	}
	if filter.ToolName == "" && filter.Status == "" {
		return nil, nil
	}
	return &filter, nil
}

func stringArrayArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

//nolint:unused // retained for internal watch-send parsing; public job_watch currently rejects send.
func watchSendArg(args map[string]any) (*watchSendArgs, error) {
	raw, ok := args["send"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("send must be an object")
	}
	if isEmptyWatchSend(values) {
		return nil, nil
	}
	to := strings.TrimSpace(stringArg(values, "to"))
	if to == "" {
		return nil, errors.New("invalid_request: send.to is required")
	}
	return &watchSendArgs{
		To:             to,
		Message:        stringArg(values, "message"),
		IncludeExcerpt: shellBoolArg(values, "include_excerpt"),
	}, nil
}

//nolint:unused // retained with watchSendArg for internal watch-send parsing.
func isEmptyWatchSend(values map[string]any) bool {
	return strings.TrimSpace(stringArg(values, "to")) == "" &&
		stringArg(values, "message") == "" &&
		!shellBoolArg(values, "include_excerpt")
}

func jobTypeArrayArg(args map[string]any, key string) ([]jobstore.JobType, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	types := make([]jobstore.JobType, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", key)
		}
		jobType := jobstore.JobType(s)
		switch jobType {
		case jobstore.JobShell, jobstore.JobType(delegateResourceType):
			types = append(types, jobType)
		default:
			return nil, fmt.Errorf("invalid job type %q", jobType)
		}
	}
	return types, nil
}

func findJobRecord(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
	recs, _, err := jm.listWithError(listFilter{IncludeNested: true})
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if rec.JobID == jobID {
			return rec, nil
		}
	}
	return nil, errJobNotFound(jobID)
}

func waitForJobDone(ctx context.Context, jm *jobManager, jobID string, timeout time.Duration) bool {
	done, ok := jobDone(jm, jobID)
	if !ok {
		return true
	}
	timer := jm.clock.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C():
		return false
	case <-ctx.Done():
		return false
	}
}

func jobDone(jm *jobManager, jobID string) (<-chan struct{}, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		return nil, false
	}
	return run.done, true
}

//nolint:unused // retained for tagged job projection state-machine owners.
func projectJobRecord(s *Session, rec *jobstore.JobRecord) jobListEntry {
	return projectJobRecordForViewer(s, s, rec)
}

func projectJobRecordForViewer(viewer *Session, assessor *Session, rec *jobstore.JobRecord) jobListEntry {
	now := time.Now()
	if viewer != nil && viewer.clock != nil {
		now = viewer.clock.Now()
	}
	if assessor != nil && assessor.jobManager != nil {
		now = assessor.jobManager.now()
	}
	return projectJobRecordAt(viewer, assessor, rec, now)
}

func projectJobRecordAt(viewer *Session, assessor *Session, rec *jobstore.JobRecord, now time.Time) jobListEntry {
	_ = viewer
	_ = assessor
	statusView := projectJobStatus(now, rec)
	return jobListEntry{
		ID:                 rec.JobID,
		JobID:              rec.JobID,
		Kind:               statusView.Kind,
		Type:               string(rec.Type),
		Status:             string(rec.Status),
		Phase:              statusView.Phase,
		Reason:             stringPtrOrNil(rec.Reason),
		Description:        jobRecordDisplayLabel(rec),
		ParentJobID:        stringPtrOrNil(rec.ParentJobID),
		ParentDelegateID:   stringPtrOrNil(rec.ParentDelegateID),
		OwnerSessionID:     rec.OwnerSessionID,
		VisibleToSessionID: rec.VisibleToSession,
		TranscriptRef:      stringPtrOrNil(statusView.TranscriptRef),
		ExhaustionBudget:   rec.ExhaustionBudget,
		ExhaustionLimit:    rec.ExhaustionLimit,
		StartedAt:          rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:            timePtrOrNil(rec.EndedAt),
		RunningForMS:       statusView.RunningForMS,
		DurationMS:         statusView.DurationMS,
		QuietForMS:         statusView.QuietForMS,
		LastEventAt:        statusView.LastEventAt,
		LastActivity:       lastActivityProjection(rec),
		ExitCode:           rec.ExitCode,
		TotalBytes:         rec.OutputBytes,
		Command:            stringPtrOrNil(rec.Command),
	}
}

func marshalBoundedJSON(v any, maxChars int) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if maxChars > 0 && jsonCharLen(b) > maxChars {
		return "", fmt.Errorf("job tool JSON output exceeds %d characters after bounding", maxChars)
	}
	return string(b), nil
}

// marshalBoundedJSONWithFit marshals v and returns (json, true, nil) when it fits
// maxChars, or ("", false, nil) when it does not — letting the caller progressively
// drop fields to fit. Used by the delegate result path, which keeps JSON output.
func marshalBoundedJSONWithFit(v any, maxChars int) (string, bool, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", false, err
	}
	if maxChars <= 0 || jsonCharLen(b) <= maxChars {
		return string(b), true, nil
	}
	return "", false, nil
}

func jobToolResultMaxChars(reg *tool.Registry, name string) int {
	if reg == nil {
		return jobToolResultDefaultMaxChar
	}
	registered := reg.Get(name)
	if registered == nil || registered.Limit.MaxChars <= 0 {
		return jobToolResultDefaultMaxChar
	}
	if registered.Limit.MaxChars < jobToolResultMinJSONChars {
		return jobToolResultMinJSONChars
	}
	return registered.Limit.MaxChars
}

func enforceJobToolJSONLimits(reg *tool.Registry) {
	if reg == nil {
		return
	}
	overrides := map[string]schema.ToolOutputLimit{}
	for _, name := range []string{"job_status", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
		registered := reg.Get(name)
		if registered == nil || registered.Limit.MaxChars >= jobToolResultMinJSONChars {
			continue
		}
		overrides[name] = schema.ToolOutputLimit{MaxChars: jobToolResultMinJSONChars, Strategy: registered.Limit.Strategy}
	}
	if len(overrides) > 0 {
		reg.OverrideLimits(overrides)
	}
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func timePtrOrNil(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.Format(time.RFC3339Nano)
	return &formatted
}

//go:fix inline
func publicJobKind(t jobstore.JobType) string {
	_ = t
	return jobKindShell
}

func shellTranscriptRef(jobID string) string {
	return "job:" + jobID
}

func defaultJobPhase(rec *jobstore.JobRecord) string {
	if rec == nil || rec.Status.IsTerminal() {
		return ""
	}
	if rec.Phase != "" {
		return rec.Phase
	}
	switch rec.Type {
	case jobstore.JobShell:
		return jobPhaseProcessRunning
	default:
		return ""
	}
}

func jobTranscriptRef(rec *jobstore.JobRecord) string {
	if rec == nil {
		return ""
	}
	if rec.Type == jobstore.JobShell && rec.JobID != "" {
		return shellTranscriptRef(rec.JobID)
	}
	return ""
}

func projectJobStatus(now time.Time, rec *jobstore.JobRecord) jobStatusResult {
	last := rec.StartedAt
	if rec.LastActivity != nil {
		last = *rec.LastActivity
	} else if rec.EndedAt != nil {
		last = *rec.EndedAt
	}

	out := jobStatusResult{
		JobID:            rec.JobID,
		Kind:             publicJobKind(rec.Type),
		Status:           string(rec.Status),
		Description:      jobRecordDisplayLabel(rec),
		Phase:            defaultJobPhase(rec),
		Reason:           stringPtrOrNil(rec.Reason),
		ExhaustionBudget: rec.ExhaustionBudget,
		ExhaustionLimit:  rec.ExhaustionLimit,
		StartedAt:        rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:          timePtrOrNil(rec.EndedAt),
		LastEventAt:      timePtrOrNil(&last),
		TranscriptRef:    jobTranscriptRef(rec),
		ExitCode:         rec.ExitCode,
	}
	if rec.Status.IsTerminal() {
		end := now
		if rec.EndedAt != nil {
			end = *rec.EndedAt
		}
		out.DurationMS = new(end.Sub(rec.StartedAt).Milliseconds())
		out.Phase = ""
	} else {
		out.RunningForMS = new(now.Sub(rec.StartedAt).Milliseconds())
		out.QuietForMS = new(now.Sub(last).Milliseconds())
	}
	return out
}

// lastActivityProjection renders a record's last_activity timestamp. A running
// job carries a live LastActivity stamp. A terminal record reloaded from the
// store has no stamp (LastActivity is in-memory only, never folded), so it
// falls back to the most recent activity it can attest: EndedAt, then
// StartedAt.
func lastActivityProjection(rec *jobstore.JobRecord) *string {
	if rec.LastActivity != nil {
		return timePtrOrNil(rec.LastActivity)
	}
	if rec.EndedAt != nil {
		return timePtrOrNil(rec.EndedAt)
	}
	started := rec.StartedAt
	return timePtrOrNil(&started)
}
