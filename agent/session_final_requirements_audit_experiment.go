package agent

import (
	"fmt"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
	taskpkg "primeradiant.com/serf/agent/task"
)

const finalRequirementsAuditDescription = "Audit original task requirements"

func finalRequirementsAuditPrompt(original string) string {
	return "The original task assigned to you was '" + original + "' - Manually check and confirm that each requirement was met and report that in your final response"
}

func (s *Session) finalRequirementsAuditEligibleSession() bool {
	return s.cfg.ExperimentalFinalRequirementsAudit &&
		s.cfg.NonInteractive &&
		!s.isSubagentSession() &&
		(s.cfg.SessionStartKind == "" || s.cfg.SessionStartKind == plugin.SessionStartKindStartup)
}

func (s *Session) maybeStartFinalRequirementsAudit() (bool, error) {
	s.mu.Lock()
	if !s.finalRequirementsAuditEligibleSession() || s.finalRequirementsAuditInjected || s.originalTaskText == "" {
		s.mu.Unlock()
		return false, nil
	}
	original := s.originalTaskText
	s.mu.Unlock()

	store := s.getOrCreateTaskStore()
	tasks := store.View()
	if len(tasks) == 0 || !taskListAllDone(tasks) {
		return false, nil
	}

	s.mu.Lock()
	if s.finalRequirementsAuditInjected {
		s.mu.Unlock()
		return false, nil
	}
	s.finalRequirementsAuditInjected = true
	s.mu.Unlock()

	added, err := store.Append([]taskpkg.TaskInput{{
		Type:        taskpkg.TaskTypeVerify,
		Description: finalRequirementsAuditDescription,
		Prompt:      finalRequirementsAuditPrompt(original),
	}})
	if err != nil {
		return false, fmt.Errorf("append final requirements audit: %w", err)
	}
	if len(added) != 1 {
		return false, fmt.Errorf("append final requirements audit: added %d tasks", len(added))
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: added[0].ID, Status: taskpkg.TaskInProgress}}); err != nil {
		return false, fmt.Errorf("start final requirements audit: %w", err)
	}

	total, done := store.Progress()
	s.emit(events.EventTaskUpdated, events.TaskUpdatedData{Total: total, Done: done})
	s.SteerKind(formatCurrentTaskSteering(added[0], s.canInstructTool("task_list")), events.SteeringKindCurrentTask)
	return true, nil
}
