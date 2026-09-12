package tui

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// hubModelForGoalResume builds a session hub model whose goal/set handler
// records params (including the Resume flag) and reports Started=true.
// lastHubGoalParams reads back the most recent call.
var task9GoalParams []appwire.GoalSetParams

func hubModelForGoalResume(t *testing.T) *hubModel {
	t.Helper()
	task9GoalParams = nil
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
			task9GoalParams = append(task9GoalParams, params)
			return appwire.GoalSetResponse{Started: params.Objective != "" || params.Resume}, nil
		})
	})
	t.Cleanup(cleanup)
	m := newSessionHubModel(client)
	return &m
}

func lastHubGoalParams(t *testing.T) appwire.GoalSetParams {
	t.Helper()
	if len(task9GoalParams) == 0 {
		t.Fatal("no goal/set call recorded")
	}
	return task9GoalParams[len(task9GoalParams)-1]
}

// Task-9 slice-3 command-surface tests (spec §7 precedence, RPC/flag, status
// format; §9 literal-collision escape). Failing until runHubGoal parses
// `resume [--extend <budget> <value>]` BEFORE objective-setting, the registry
// gains +resume, and the status line renders the full §7 shape.

// TestHubGoalResumeNeverSetsLiteral pins the §7 precedence: a blocked goal's
// `/goal resume` must reach the daemon as a resume (not GoalSet("resume"),
// which per §1 retarget semantics would destroy the waits/timers/ledger it
// should recover).
func TestHubGoalResumeNeverSetsLiteral(t *testing.T) {
	m := hubModelForGoalResume(t)
	cmd := m.runHubGoal("resume")
	if cmd == nil {
		t.Fatal("/goal resume should produce a cmd")
	}
	msg := cmd()
	goalMsg, ok := msg.(hubGoalMsg)
	if !ok || goalMsg.err != nil {
		t.Fatalf("msg = %#v, want a successful hubGoalMsg", msg)
	}
	if !goalMsg.resumed {
		t.Fatalf("msg = %#v, want resumed=true (a resume RPC/flag, never a literal set)", goalMsg)
	}
	if got := lastHubGoalParams(t); got.Objective == "resume" && !got.Resume {
		t.Fatalf("params = %+v: /goal resume became the literal objective \"resume\"", got)
	}
}

// TestHubGoalResumeExtendParsesTwoTokenGrammar pins the --extend arity (R6 K):
// `/goal resume --extend continuations 50` parses the two-token renewal and
// carries no objective text.
func TestHubGoalResumeExtendParsesTwoTokenGrammar(t *testing.T) {
	m := hubModelForGoalResume(t)
	cmd := m.runHubGoal("resume --extend continuations 50")
	if cmd == nil {
		t.Fatal("/goal resume --extend should produce a cmd")
	}
	if msg := cmd(); msg.(hubGoalMsg).err != nil {
		t.Fatalf("msg = %#v, want success", msg)
	}
	got := lastHubGoalParams(t)
	if !got.Resume {
		t.Fatalf("params = %+v, want Resume=true", got)
	}
	if got.ExtendBudget != "continuations" || got.ExtendValue != 50 {
		t.Fatalf("params = %+v, want extend continuations/50", got)
	}
	if got.Objective != "" {
		t.Fatalf("params = %+v, want no objective text after a bare --extend", got)
	}
}

// TestHubGoalResumeWithTextSetsObjective pins the §9 bullet: `/goal resume
// <text>` with objective text resumes AND sets it.
func TestHubGoalResumeWithTextSetsObjective(t *testing.T) {
	m := hubModelForGoalResume(t)
	cmd := m.runHubGoal("resume --extend deadline 3600 finish the deploy")
	if cmd == nil {
		t.Fatal("/goal resume with text should produce a cmd")
	}
	if msg := cmd(); msg.(hubGoalMsg).err != nil {
		t.Fatalf("msg = %#v, want success", msg)
	}
	got := lastHubGoalParams(t)
	if !got.Resume || got.Objective != "finish the deploy" {
		t.Fatalf("params = %+v, want Resume=true with the objective text", got)
	}
	if got.ExtendBudget != "deadline" || got.ExtendValue != 3600 {
		t.Fatalf("params = %+v, want extend deadline/3600", got)
	}
}

// TestHubGoalLiteralCollision pins the literal-collision escape (R5 J-I1, R6
// K): bare `resume`/`status`/`clear` are subcommands, never objectives — and
// a resume-shaped word anywhere but first is still an objective.
func TestHubGoalLiteralCollision(t *testing.T) {
	m := hubModelForGoalResume(t)
	for _, sub := range []string{"resume", "Resume", "RESUME", "status", "Status", "clear", "CLEAR"} {
		cmd := m.runHubGoal(sub)
		if sub == "status" || sub == "Status" {
			if cmd != nil {
				t.Fatalf("/goal %s should render locally with no cmd", sub)
			}
			continue
		}
		if cmd == nil {
			t.Fatalf("/goal %s should produce a cmd (a subcommand, never the literal objective)", sub)
		}
		if msg := cmd(); msg.(hubGoalMsg).err != nil {
			t.Fatalf("/goal %s msg = %#v, want success", sub, msg)
		}
	}
	// "please resume the deploy" is an objective, not the resume subcommand.
	cmd := m.runHubGoal("please resume the deploy")
	if cmd == nil {
		t.Fatal("objective text containing resume should produce a set cmd")
	}
	if msg := cmd(); msg.(hubGoalMsg).err != nil {
		t.Fatalf("objective resume msg = %#v, want success", msg)
	}
	if got := lastHubGoalParams(t); got.Resume || got.Objective != "please resume the deploy" {
		t.Fatalf("params = %+v, want a plain set of the full text", got)
	}
	// "resumed the deploy" is an objective, not the resume subcommand.
	cmd = m.runHubGoal("resumed the deploy")
	if cmd == nil {
		t.Fatal("objective text starting with resumed should produce a set cmd")
	}
	if msg := cmd(); msg.(hubGoalMsg).err != nil {
		t.Fatalf("objective resumed msg = %#v, want success", msg)
	}
	if got := lastHubGoalParams(t); got.Resume || got.Objective != "resumed the deploy" {
		t.Fatalf("params = %+v, want a plain set of the full text", got)
	}
}

// TestHubGoalStatusFullShape pins the §7 status format: "Goal: <status>
// <used/max> · waiting on <labels> · <nearest deadline> · <stage>".
func TestHubGoalStatusFullShape(t *testing.T) {
	got := hubGoalStatusText(&appwire.GoalState{
		Status:            "blocked",
		UsedContinuations: 7,
		MaxContinuations:  200,
		Stage:             "nudged",
	})
	want := "Goal: blocked 7/200 · nudged"
	if got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	waiting := hubGoalStatusText(&appwire.GoalState{
		Status:            "waiting",
		UsedContinuations: 3,
		MaxContinuations:  200,
		WaitingOn: []appwire.GoalWaitState{
			{WaitID: "wait_1", Label: "alpha", DeadlineUnixMilli: 2000},
			{WaitID: "wait_2", Label: "beta", DeadlineUnixMilli: 3000},
		},
		NearestLabel:             "alpha",
		NearestDeadlineUnixMilli: 2000,
		Stage:                    "",
	})
	wantWaiting := "Goal: waiting 3/200 · waiting on alpha, beta · 2000"
	if waiting != wantWaiting {
		t.Fatalf("status = %q, want %q", waiting, wantWaiting)
	}
	full := hubGoalStatusText(&appwire.GoalState{
		Status:            "waiting",
		UsedContinuations: 3,
		MaxContinuations:  200,
		WaitingOn: []appwire.GoalWaitState{
			{WaitID: "wait_1", Label: "alpha", DeadlineUnixMilli: 2000},
		},
		NearestLabel:             "alpha",
		NearestDeadlineUnixMilli: 2000,
		Stage:                    "auto-parked",
	})
	wantFull := "Goal: waiting 3/200 · waiting on alpha · 2000 · auto-parked"
	if full != wantFull {
		t.Fatalf("status = %q, want %q", full, wantFull)
	}
	if !strings.Contains(hubGoalStatusText(nil), "/goal") {
		t.Fatalf("nil-goal hint = %q, want the /goal usage hint", hubGoalStatusText(nil))
	}
}
