//go:build serffuzz

package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzWvValidateSendTarget drives validateWatchSendDeliveryTarget — the pure
// install-time validation decision tree lifted out of validateWatchSendTarget —
// against a fuzzed in-memory delegate/job world exposed through injected resolver
// closures, so the pure validation runs without touching a store.
//
// Oracles (beyond never-panic):
//   - determinism: the same (target, resolver world) yields the same verdict;
//   - total: every target yields either nil or a non-empty typed error;
//   - the caller alias always validates to nil;
//   - cross-check consistency: a target this rejects with not_controllable or
//     target_not_messageable must NOT classify as watchSendDelivered via the
//     restore-time twin classifyWatchSendDeliveryTarget over the same world.
func FuzzWvValidateSendTarget(f *testing.F) {
	f.Add("dlg_1", "sess_a", false, false, uint8(0), uint8(1), true, true, true, true, false, true, false)
	f.Add("dlg_2", "sess_a", true, false, uint8(4), uint8(0), true, false, true, true, false, false, false)
	f.Add("job_9", "sess_a", false, false, uint8(0), uint8(0), false, false, false, false, false, false, false)
	f.Add("dlg_x", "sess_a", false, true, uint8(1), uint8(0), true, true, false, true, false, true, true)
	f.Add("", "", false, false, uint8(0), uint8(0), false, false, false, false, true, false, false)

	f.Fuzz(func(t *testing.T, target, sessionID string,
		delForeign, jobForeign bool, delStatusSel, jobStatusSel uint8,
		delResumable, jobResumablePtr, jobIsDelegate, delegateExists, loadErr, findErr, assessResumable bool) {

		delStatuses := []jobstore.DelegateStatus{
			jobstore.DelegateRunning, jobstore.DelegateDriving, jobstore.DelegateIdle,
			jobstore.DelegateStopped, jobstore.DelegateNotResumable,
		}
		jobStatuses := []jobstore.Status{
			jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusCancelled, jobstore.StatusStopped,
		}
		delOwner := sessionID
		if delForeign {
			delOwner = "other_" + sessionID
		}
		jobOwner := sessionID
		if jobForeign {
			jobOwner = "other_" + sessionID
		}
		jobType := jobstore.JobShell
		if jobIsDelegate {
			jobType = jobstore.JobDelegate
		}

		res := watchSendTargetResolver{
			sessionID:     sessionID,
			hasJobManager: true,
			loadDelegates: func() (map[string]*jobstore.DelegateRecord, error) {
				if loadErr {
					return nil, errors.New("load boom")
				}
				m := map[string]*jobstore.DelegateRecord{}
				if delegateExists {
					m[target] = &jobstore.DelegateRecord{
						DelegateID:     target,
						OwnerSessionID: delOwner,
						Status:         delStatuses[int(delStatusSel)%len(delStatuses)],
						Resumable:      delResumable,
						CurrentJobID:   "job_cur",
						LatestJobID:    "job_latest",
					}
				}
				return m, nil
			},
			findJobRecord: func(jobID string) (*jobstore.JobRecord, error) {
				if findErr {
					return nil, errors.New("find boom")
				}
				rec := &jobstore.JobRecord{
					JobID:          jobID,
					Type:           jobType,
					Status:         jobStatuses[int(jobStatusSel)%len(jobStatuses)],
					OwnerSessionID: jobOwner,
				}
				if jobResumablePtr {
					b := true
					rec.Resumable = &b
				}
				return rec, nil
			},
			assessResumable: func(*jobstore.JobRecord) delegateResumability {
				return delegateResumability{Resumable: assessResumable}
			},
		}

		err := validateWatchSendDeliveryTarget(target, watchArgs{}, res)
		err2 := validateWatchSendDeliveryTarget(target, watchArgs{}, res)
		if (err == nil) != (err2 == nil) || (err != nil && err.Error() != err2.Error()) {
			t.Fatalf("non-deterministic: %v vs %v", err, err2)
		}
		if err != nil && err.Error() == "" {
			t.Fatalf("rejection must carry a non-empty message for %q", target)
		}

		if got := validateWatchSendDeliveryTarget(runtimeMessageAliasCaller, watchArgs{}, res); got != nil {
			t.Fatalf("caller alias must always validate to nil, got %v", got)
		}

		if err != nil && (strings.Contains(err.Error(), "not_controllable") || strings.Contains(err.Error(), "target_not_messageable")) {
			class, _ := classifyWatchSendDeliveryTarget(target, res)
			if class == watchSendDelivered {
				t.Fatalf("target %q rejected as %q but classified as delivered", target, err.Error())
			}
		}
	})
}
