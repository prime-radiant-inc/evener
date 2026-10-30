package agent

import (
	"errors"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

type createShellOpts struct {
	Command     string
	Intent      string
	Description string
}

func (jm *jobManager) createShell(opts createShellOpts) (*jobstore.JobRecord, error) {
	release, err := jm.beginRetirementMutation("job")
	if err != nil {
		return nil, err
	}
	defer release()
	startedAt := jm.now()
	jobID, outputPath, output, err := jm.createJobOutput()
	if err != nil {
		return nil, err
	}
	parentJobID, parentDelegateID := jm.currentJobParent()
	jobProvenance := jm.currentCausalProvenance()
	rec := &jobstore.JobRecord{
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		Command:          opts.Command,
		Intent:           opts.Intent,
		Description:      opts.Description,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		ParentJobID:      parentJobID,
		ParentDelegateID: parentDelegateID,
		StartedAt:        startedAt,
		Phase:            jobPhaseProcessRunning,
		LastActivity:     &startedAt,
		OutputPath:       outputPath,
		Provenance:       provenance.Clone(jobProvenance),
	}
	run := &runningJob{
		rec:            rec,
		output:         output,
		signal:         func() {},
		done:           make(chan struct{}),
		durableStarted: true,
	}

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		_ = output.Close()
		_ = jobstore.RemoveOutputArtifacts(outputPath)
		return nil, errJobManagerClosing
	}
	started := jobStartedEvent(rec)
	if err := jm.appendEvent(started); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		_ = jobstore.RemoveOutputArtifacts(outputPath)
		return nil, err
	}
	if err := jm.forwardLocked(started); err != nil {
		_ = output.Close()
		if terminalErr := jm.appendStartForwardFailure(rec.JobID, output, rec.Provenance); terminalErr != nil {
			run.forwardDisabled = true
			jm.running[jobID] = run
			jm.mu.Unlock()
			go jm.finalizeShellAsync(jobID, jobstore.StatusFailed, "forward_failed", nil)
			return nil, errors.Join(err, terminalErr)
		}
		jm.mu.Unlock()
		return nil, errors.Join(errDelayedShellStartForwardFailed, err)
	}
	jm.running[jobID] = run
	jm.mu.Unlock()
	jm.emitJobStarted(started, run)
	return rec, nil
}
