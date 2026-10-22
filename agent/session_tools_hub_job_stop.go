package agent

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
)

// StopJobForManager delegates to the ordinary human job_stop owner. The
// preflight prevents a refused terminal or unknown target from stopping its
// children before the owner can report the refusal.
func (s *Session) StopJobForManager(ctx context.Context, params appwire.SessionJobStopParams) (appwire.SessionJobStopResponse, error) {
	if !strings.HasPrefix(params.JobID, "job_") || params.MaxWaitMS < 0 {
		return appwire.SessionJobStopResponse{}, appwire.InvalidParams("job stop requires a job_ target and non-negative maxWaitMs")
	}
	_, record, err := s.nestedOrLocalJobManager(params.JobID)
	if err != nil {
		return appwire.SessionJobStopResponse{}, err
	}
	if record == nil || record.Status.IsTerminal() {
		return appwire.SessionJobStopResponse{}, errors.New("job is already terminal")
	}
	args := map[string]any{"target": params.JobID, "include_children": params.IncludeChildren, "max_wait_ms": params.MaxWaitMS}
	result, err := jobStopTool(ctx, s, args, 0)
	if err != nil {
		return appwire.SessionJobStopResponse{}, err
	}
	state := result.(tool.StateResult).State.(jobStopResult)
	return appwire.SessionJobStopResponse{JobID: state.JobID, Status: state.Status, Outcome: state.Outcome}, nil
}

// StopOwnedDelegateForManager uses the human job_stop dlg_ route, not the
// distinct single-run DelegateStopParams RPC.
func (s *Session) StopOwnedDelegateForManager(ctx context.Context, params appwire.SessionOwnedDelegateStopParams) (appwire.SessionJobStopResponse, error) {
	if !strings.HasPrefix(params.DelegateID, "dlg_") || params.MaxWaitMS < 0 {
		return appwire.SessionJobStopResponse{}, appwire.InvalidParams("owned delegate stop requires a dlg_ target and non-negative maxWaitMs")
	}
	if s.delegateController != nil {
		snapshots := s.delegateController.Snapshot().rows
		byID := make(map[string]delegateSnapshot, len(snapshots))
		for _, row := range snapshots {
			byID[row.id] = row
		}
		if target, found := byID[params.DelegateID]; found && !target.currentRunOpen && target.pendingStopSeq == 0 {
			settled := true
			for _, row := range snapshots {
				for parent := row.parentID; parent != ""; {
					if parent == target.id && (row.currentRunOpen || row.pendingStopSeq != 0) {
						settled = false
						break
					}
					ancestor, ok := byID[parent]
					if !ok || ancestor.parentID == parent {
						break
					}
					parent = ancestor.parentID
				}
			}
			if settled {
				return appwire.SessionJobStopResponse{JobID: params.DelegateID, Status: string(target.lifecycle), Outcome: "already_stopped"}, nil
			}
		}
	}
	result, err := jobStopTool(ctx, s, map[string]any{"target": params.DelegateID, "max_wait_ms": params.MaxWaitMS}, 0)
	if err != nil {
		return appwire.SessionJobStopResponse{}, err
	}
	state := result.(tool.StateResult).State.(jobStopResult)
	return appwire.SessionJobStopResponse{JobID: state.JobID, Status: state.Status, Outcome: state.Outcome}, nil
}
