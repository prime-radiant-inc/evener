package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// terminalReportCandidate is one accepted terminal communicate call's disposition and
// target, before it is admitted or refused at finalization.
type terminalReportCandidate struct {
	Disposition string
	Packet      delegatestore.TerminalPacket
	Blocker     string
	WaitingOn   string
}

var (
	errTerminalReportRefused = errors.New("terminal report refused")
	errDelegateWaitResolved  = errors.New("delegate wait resolved")
)

// waitForTerminalTarget keeps the run alive until the named waker signals or
// disappears. Any session notification counts: a job completion, a watch
// frame, or a child delivery all wake the run to inspect the new evidence.
func (a *subagent) waitForTerminalTarget(ctx context.Context, target string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if a.sess.terminalWaitSatisfied(ctx, target) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Session) terminalWaitSatisfied(ctx context.Context, target string) bool {
	if s == nil {
		return true
	}
	if s.peekNotifications() > 0 {
		return true
	}
	if s.hasPendingSteering() {
		return true
	}
	if pending, err := s.pendingDelegateAttentionIDs(); err == nil && len(pending) != 0 {
		return true
	}
	if len(s.pendingChildDeliveryIDs()) != 0 {
		return true
	}
	if s.hasQueuedWaitInput(target) {
		return true
	}
	return s.validateWaitTarget(ctx, target) != nil
}

func (s *Session) hasQueuedWaitInput(target string) bool {
	kind, id := splitWaitTarget(target)
	if kind != "" && kind != "input" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, input := range s.inputQueue {
		if input.ID == id {
			return true
		}
	}
	for _, steering := range s.steeringQueue {
		if steering.StableTurnID == id || steering.ClientMutationID == id {
			return true
		}
	}
	return false
}

// admitTerminalReport decides whether a done or blocked report may publish.
// A done report requires an empty, known outstanding-work inventory. A blocked
// report is admitted with live work and carries that inventory beside it. A
// refusal steers the delegate once and leaves the run open; it returns
// admitted=false with a nil error. A non-nil error means the refusal itself
// could not be delivered.
func (a *subagent) admitTerminalReport(ctx context.Context, candidate *terminalReportCandidate) (bool, error) {
	return a.admitTerminalReportWithInventory(ctx, candidate)
}

func (a *subagent) admitTerminalReportWithInventory(ctx context.Context, candidate *terminalReportCandidate) (bool, error) {
	if a == nil || a.sess == nil {
		return false, errors.New("terminal admission requires a delegate session")
	}
	if candidate == nil {
		return false, errors.New("terminal admission requires a candidate")
	}
	switch candidate.Disposition {
	case tool.CommunicateEndReasonDone, tool.CommunicateEndReasonBlocked:
	case tool.CommunicateEndReasonWaitingOnWork:
		return false, appwire.InvalidParams("waiting_on_work is not a terminal admission")
	default:
		return false, appwire.InvalidParams(fmt.Sprintf("unknown delegate disposition %q", candidate.Disposition))
	}

	work, err := a.sess.outstandingDelegateWorkForAdmission()
	if err != nil || len(work.Unknown) != 0 {
		reason := "The terminal report was refused because outstanding work could not be verified"
		if err != nil {
			reason += ": " + err.Error()
		}
		reason += ". Outstanding: " + strings.Join(work.IDs(), ", ")
		if steerErr := a.sess.steerTerminalRefusal(reason); steerErr != nil {
			return false, steerErr
		}
		return false, nil
	}

	if candidate.Disposition == tool.CommunicateEndReasonDone {
		if !work.Empty() {
			reason := "The terminal report was refused because this run still has outstanding work: " +
				strings.Join(work.IDs(), ", ") +
				". Finish or settle that work, then report done."
			if steerErr := a.sess.steerTerminalRefusal(reason); steerErr != nil {
				return false, steerErr
			}
			return false, nil
		}
		a.mu.Lock()
		a.blockedWork = nil
		a.mu.Unlock()
		return true, nil
	}

	if strings.TrimSpace(candidate.Blocker) == "" {
		return false, appwire.InvalidParams("communicate end_reason blocked requires blocker")
	}
	ids := work.IDs()
	a.mu.Lock()
	a.blockedWork = append([]string(nil), ids...)
	a.mu.Unlock()
	if len(ids) != 0 {
		candidate.Packet.Metadata = packetMetadataWithOutstandingWork(candidate.Packet.Metadata, ids)
	}
	return true, nil
}

// admitTerminalWait accepts a waiting_on_work report only when the named waker
// is live and has an existing completion or cancellation path. An accepted wait
// publishes no packet and leaves the run open.
func (a *subagent) admitTerminalWait(ctx context.Context, candidate *terminalReportCandidate) (bool, error) {
	if a == nil || a.sess == nil {
		return false, errors.New("terminal wait requires a delegate session")
	}
	if candidate == nil || strings.TrimSpace(candidate.WaitingOn) == "" {
		if err := a.sess.steerTerminalRefusal("The wait was refused because it names no job, watch, child delegate, or queued input."); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := a.sess.validateWaitTarget(ctx, candidate.WaitingOn); err != nil {
		if steerErr := a.sess.steerTerminalRefusal("The wait was refused: " + err.Error() + ". Report done, blocked, or wait on a live target."); steerErr != nil {
			return false, steerErr
		}
		return false, nil
	}
	return true, nil
}

// validateWaitTarget resolves a wait target against a live waker. It accepts a
// bare identity or a kind-prefixed one (job:, watch:, child:, input:).
func (s *Session) validateWaitTarget(ctx context.Context, target string) error {
	if s == nil {
		return errors.New("no session")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("wait target is empty")
	}
	kind, id := splitWaitTarget(target)
	if id == "" {
		return fmt.Errorf("wait target %q has no identity", target)
	}
	_ = ctx

	if kind == "" || kind == "job" {
		if s.jobManager != nil {
			s.jobManager.mu.Lock()
			_, running := s.jobManager.running[id]
			s.jobManager.mu.Unlock()
			if running {
				return nil
			}
			journal, err := retirementJobJournal(filepath.Join(s.jobManager.dir, "jobs.jsonl"))
			if err != nil {
				return fmt.Errorf("cannot verify job %q: %w", id, err)
			}
			for _, record := range jobstore.Fold(journal) {
				if record.JobID == id && !record.Status.IsTerminal() {
					return nil
				}
			}
		}
	}

	if kind == "" || kind == "watch" {
		if slices.Contains(s.receiverWatchIDs(), id) {
			return nil
		}
	}

	if kind == "" || kind == "child" {
		if s.delegateController != nil {
			s.delegateController.mu.Lock()
			_, descendant := s.delegateController.delegateSubtreeIDsLocked(s.owningDelegateID)[id]
			aggregate := s.delegateController.durable[id]
			live := descendant && aggregate != nil && (aggregate.CurrentRunOpen ||
				aggregate.Phase == delegatestore.PhaseRunning ||
				aggregate.Phase == delegatestore.PhaseSettling ||
				aggregate.Phase == delegatestore.PhaseStopping ||
				len(aggregate.PendingDeliveries) != 0)
			s.delegateController.mu.Unlock()
			if live {
				return nil
			}
		}
	}

	if kind == "" || kind == "input" {
		s.mu.Lock()
		for _, input := range s.inputQueue {
			if input.ID == id {
				s.mu.Unlock()
				return nil
			}
		}
		for _, steering := range s.steeringQueue {
			if steering.StableTurnID == id || steering.ClientMutationID == id {
				s.mu.Unlock()
				return nil
			}
		}
		s.mu.Unlock()
	}

	return fmt.Errorf("wait target %q is absent or already settled", target)
}

func splitWaitTarget(target string) (kind, id string) {
	prefix, rest, ok := strings.Cut(target, ":")
	if !ok {
		return "", target
	}
	switch prefix {
	case "job", "watch", "child", "input":
		return prefix, rest
	default:
		return "", target
	}
}

func (s *Session) steerTerminalRefusal(reason string) error {
	if s == nil {
		return errors.New("no session to steer")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "The terminal report was refused."
	}
	if err := s.SteerKind(reason, events.SteeringKindNoToolCalls); err != nil {
		return fmt.Errorf("steer terminal refusal: %w", err)
	}
	return nil
}

func packetMetadataWithOutstandingWork(metadata json.RawMessage, ids []string) json.RawMessage {
	fields := map[string]json.RawMessage{}
	if len(metadata) != 0 {
		if err := json.Unmarshal(metadata, &fields); err != nil {
			return metadata
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return metadata
	}
	fields["outstanding_work"] = raw
	merged, err := json.Marshal(fields)
	if err != nil {
		return metadata
	}
	return merged
}
