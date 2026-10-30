package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func (jm *jobManager) setParentJobID(jobID string) {
	jm.mu.Lock()
	jm.parentJobID = jobID
	jm.mu.Unlock()
}

func (jm *jobManager) createJobOutputForID(jobID string) (string, *jobstore.OutputStore, error) {
	ownerSessionID, err := identifier.JobOwnerSessionID(jobID)
	if err != nil {
		return "", nil, err
	}
	if ownerSessionID != jm.sessionID {
		return "", nil, fmt.Errorf("job ID owner %q does not match session %q", ownerSessionID, jm.sessionID)
	}
	records, err := jm.store.Load()
	if err != nil {
		return "", nil, err
	}
	if _, exists := records[jobID]; exists {
		return "", nil, fmt.Errorf("job ID %q already exists: %w", jobID, os.ErrExist)
	}
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jm.createOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		return "", nil, err
	}
	return outputPath, output, nil
}

func (jm *jobManager) grepOutput(jobID string, re *regexp.Regexp) ([]jobstore.Match, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		// Scan the full retained output: maxJobOutputRetentionBytes caps retention, so it
		// doubles as the scan budget; the result stays bounded by maxJobGrepMatches and maxJobGrepLineBytes.
		return run.output.GrepLimitLineBytes(re, maxJobOutputRetentionBytes, maxJobGrepMatches, maxJobGrepLineBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	rec := recs[jobID]
	if rec == nil {
		return nil, errJobNotFound(jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	// Full retained scan (same budget rationale as above).
	return grepOutputFile(path, re, maxJobOutputRetentionBytes, rec)
}

func isRootOnlyJobPresenceTool(name string) bool {
	return hasString(rootOnlyJobPresenceTools, name)
}

func localEnvPolicyFromName(name string) (execenv.EnvVarPolicy, bool) {
	switch strings.TrimSpace(name) {
	case "all":
		return execenv.EnvPolicyAll, true
	case "none":
		return execenv.EnvPolicyNone, true
	case "core_only":
		return execenv.EnvPolicyCoreOnly, true
	case "default":
		return execenv.EnvPolicyDefault, true
	default:
		return execenv.EnvPolicyDefault, false
	}
}

func projectStableLiveActivityTree(clock *jobActivityClock, rootID string, load func() (*activitySessionSnapshot, int, int, error)) (appwire.JobActivityTree, error) {
	return projectStableLiveActivityTreeAt(clock, rootID, time.Now().UTC(), load)
}

func projectActivitySession(snapshot activitySessionSnapshot, budget *activityBudget) appwire.JobActivitySession {
	return projectActivitySessionAt(snapshot, budget, 0, nil, 0)
}

func delegateQuietAttentionID(lease delegateLease) string {
	return delegateQuietAttentionIDForStretch(lease, 1)
}

func (c *delegateTreeController) completionSnapshot(lease delegateLease) (delegateCompletionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	evidence, err := c.completionEvidenceLocked(lease)
	if err != nil {
		return delegateCompletionSnapshot{}, err
	}
	snapshot := delegateCompletionSnapshot{
		requirement:  evidence.requirement,
		outcome:      evidence.outcome,
		terminalSeen: evidence.terminalSeen,
	}
	if evidence.fallback != nil {
		fallback := cloneDelegateFinish(*evidence.fallback)
		snapshot.fallback = &fallback
	}
	return snapshot, nil
}

func (c *delegateTreeController) capacityInUse() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnsInUse, c.drivesInUse
}

func parseRetainedReadArgs(args map[string]any) (retainedReadArgs, retainedReadOperation, error) {
	parsed, operation, issues := parseRetainedReadArgsWithIssues(args)
	if len(issues) > 0 {
		return retainedReadArgs{}, retainedReadDefault, errors.New("invalid_request: " + issues[0].Reason)
	}
	return parsed, operation, nil
}

func (s *Session) communicateStructuredResult() (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.structured, s.comm.structured != nil
}

func (c *delegateTreeController) noteDelegateAttention(delegateID, attentionID string) bool {
	if c == nil {
		return false
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return false
	}
	defer release()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.noteDelegateAttentionLocked(delegateID, attentionID)
}

func (c *delegateTreeController) nextIdleDelegateAttention() (string, string, bool) {
	if c == nil {
		return "", "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextIdleDelegateAttentionLocked()
}

func asideSessionFS(fs afero.Fs, stateDir, parentID string) (string, error) {
	return asideSessionFSWithConfig(fs, stateDir, parentID, nil)
}

func jobWatchTool(s *Session, args map[string]any, maxChars int) (any, error) {
	return jobWatchToolWithContext(context.Background(), s, args, maxChars)
}

func (s *Session) completeClientMutationInterruptedTurn(clientMutationID string) error {
	_, err := s.completeClientMutationTurnWithState(clientMutationID, "interrupted")
	return err
}

func (c *delegateTreeController) inputPersistFailureBatch(lease delegateLease, inputErr error) (delegatestore.Event, delegatestore.Event) {
	message := "input persistence failed"
	if inputErr != nil {
		message += ": " + inputErr.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	raw, _ := json.Marshal(message)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: raw}
	return terminalFinishBatch(lease, delegatestore.OutcomeFailed, "input_persist_failed", c.now(), packet)
}

func (jm *jobManager) shellTerminal(run *runningJob, exitCode int, timedOut bool, waitErr error) (jobstore.Status, string, *int) {
	return jm.shellTerminalWithSignal(run, exitCode, "", timedOut, waitErr)
}

func (jm *jobManager) watchCount() int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.watches)
}

// LoadSessionHistoricalJobRecords reads one local session's durable jobs.jsonl and
// returns the folded job records needed by cold UI projections. It is read-only:
// a session with no jobs.jsonl yields an empty map and creates no file.
func LoadSessionHistoricalJobRecords(stateDir, sessionID string) (map[string]HistoricalJobRecord, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]HistoricalJobRecord{}, nil
		}
		return nil, err
	}

	events, err := jobstore.ReadEvents(path)
	if err != nil {
		return nil, err
	}
	records := jobstore.Fold(events)
	out := make(map[string]HistoricalJobRecord, len(records))
	for jobID, rec := range records {
		if rec == nil {
			continue
		}
		out[jobID] = HistoricalJobRecord{
			JobID:            rec.JobID,
			Type:             string(rec.Type),
			Status:           string(rec.Status),
			Reason:           rec.Reason,
			Task:             rec.Task,
			OriginTurnID:     rec.OriginTurnID,
			OriginToolCallID: rec.OriginToolCallID,
			OriginItemID:     rec.OriginItemID,
			OutputBytes:      rec.OutputBytes,
		}
	}
	return out, nil
}

// resolveLiveModelProfile fills profile's live model metadata (context window,
// reasoning support, etc.) from client's model listing when available.
// Fails open (returns profile unchanged) on a nil client/profile or any
// enumeration error — used by callers that don't need the enumerated list or
// fetch result; resolveLiveModelProfileValidated is the membership-checking
// counterpart used by NewSession.
func resolveLiveModelProfile(ctx context.Context, client *llm.Client, profile *provider.Profile) *provider.Profile {
	filled, _ := fillLiveModelMetadata(ctx, client, profile)
	return filled
}

// HistoricalJobRecord is a flattened, read-only snapshot of one job as it was
// persisted in a session's durable jobs.jsonl — the origin coordinates, the
// delegate/task it ran, and its terminal status — for tooling that inspects
// past sessions without replaying their event streams.
type HistoricalJobRecord struct {
	JobID            string
	Type             string
	Status           string
	Reason           string
	DelegateID       string
	Task             string
	TranscriptRef    string
	OriginTurnID     string
	OriginToolCallID string
	OriginItemID     string
	OutputBytes      int64
}
