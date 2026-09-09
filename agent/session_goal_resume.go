package agent

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/goal"
)

// GoalResumeFromWire is the daemon glue for goal/set with Resume=true (spec
// §7): it maps the wire --extend budget token/value (plain types — the server
// and daemon packages must not import agent internals) onto a ResumeRequest
// and runs the resume. Unknown budgets and non-positive values are errors
// naming the fault. A non-empty objective retargets atomically inside the
// resume (goalResumeWithRetarget under one goalUpdateMu hold): the kick — if
// any — renders the final objective, so the serve loop can never consume a
// stale old-objective continuation first ("/goal resume <text>" sets it).
func (s *Session) GoalResumeFromWire(objective, extendBudget string, extendValue int64) (bool, error) {
	req := goal.ResumeRequest{}
	if strings.TrimSpace(extendBudget) != "" || extendValue != 0 {
		// Single budget-name grammar (goal.ParseExtendBudget): the TUI
		// forwards its parsed token for validation here.
		budget, err := goal.ParseExtendBudget(extendBudget)
		if err != nil {
			return false, err
		}
		if extendValue <= 0 {
			return false, fmt.Errorf("invalid --extend value %d: want a positive integer (turns for continuations, seconds for deadline/parked-total)", extendValue)
		}
		req.Extend = &goal.ExtendRequest{Budget: budget, Value: extendValue}
	}
	return s.goalResumeWithRetarget(req, objective, s.sclock().Now())
}
