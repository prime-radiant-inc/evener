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

// moveLegacyMemoryIndex moves the migrated hand-written index at legacy,
// whose bytes the migration read as raw, to a backup in root, trying the
// name backup first (see backUpMemoryIndex), then removes MEMORY.md only if
// it still holds raw (see removeMigratedMemoryIndex). No lock guards the
// move: another migration, or an older build writing MEMORY.md again, may
// change the scope at any point, and neither step relies on it holding
// still. A failure leaves the index in place for the next run.
func moveLegacyMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte, backup string) error {
	return moveLegacyMemoryIndexCtx(context.Background(), env, root, legacy, raw, backup)
}

// backUpMemoryIndex keeps raw, an index's bytes, as a backup in root, trying
// the name backup first, unless a backup already holds raw byte for byte, so
// alternating builds that write the same index add nothing. The backup is a
// copy of its own, so nothing written to MEMORY.md later reaches it. A
// backup is never replaced or removed, since each holds what a person or an
// older build wrote: the copy is written in full under a private name, then
// hard-linked to the backup name, which fails rather than replace a file put
// there since the name was chosen, and a taken name moves on to the next free
// one. Any other link failure, such as a filesystem without hard links, is
// returned.
func backUpMemoryIndex(env *execenv.LocalExecutionEnvironment, root string, raw []byte, backup string) error {
	return backUpMemoryIndexCtx(context.Background(), env, root, raw, backup)
}

// removeMigratedMemoryIndex removes the index at legacy only if it holds
// raw. A read followed by an unlink could delete a rewrite landing between
// the two, and no filesystem call removes a name only if its contents match.
// So whatever legacy names is first renamed, atomically, to a name only this
// run uses: a rewrite either lands before the rename and is captured, or
// after it, as a new MEMORY.md that stays. The captured file is removed only
// if it holds raw. Otherwise it goes back to MEMORY.md, or, when MEMORY.md
// was written yet again meanwhile, to a backup of its own; either way
// errLegacyMemoryIndexChanged reports that an index is left for the next
// run. A captured file that can't be put back keeps its private name, where
// the next migration finds it.
func removeMigratedMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte) error {
	return removeMigratedMemoryIndexCtx(context.Background(), env, root, legacy, raw)
}

// migrateMemoryScope moves a scope's hand-written index (see
// legacyMemoryIndexes) into its pages: each linked page with no description
// gets the one its index line gave, then the index is moved to a backup
// (see moveLegacyMemoryIndex). It is idempotent and takes no lock (the write
// loop and moveLegacyMemoryIndex say why), and the run that finds the index
// already moved is done. A linked target that can't be read (missing, a directory, a
// refused symlink), or whose frontmatter does not parse or can't take the
// description in place, is skipped. A page that fails to write does not stop
// the others; any failed write, or failing to read or move the index,
// returns an error and leaves the index in place, so the next run finishes
// the job.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	return migrateMemoryScopeCtx(context.Background(), env)
}

// migrateLegacyMemoryIndexes is migrateMemoryScope's work for the root
// indexes legacyMemoryIndexes found, in that order.
func migrateLegacyMemoryIndexes(env *execenv.LocalExecutionEnvironment, legacies []string) error {
	return migrateLegacyMemoryIndexesCtx(context.Background(), env, legacies)
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
