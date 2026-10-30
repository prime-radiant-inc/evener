package schema

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestNewTurnFieldsRoundTripAndStayInKeyOrder(t *testing.T) {
	ordinal := uint64(7)
	turn := Turn{
		Communicate:     &CommunicateInfo{CallID: "c1", EndTurn: true, Message: "hi"},
		Completion:      &TurnCompletionInfo{Status: TurnInterrupted, CompletedAt: time.Unix(5, 0).UTC(), DurationMS: 12},
		Format:          TurnFormatIdentity,
		Kind:            TurnCompletion,
		Message:         llm.Message{Role: llm.RoleUser},
		Model:           "gpt-5.4",
		Notice:          &NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete", Iterations: 2}},
		OriginalOrdinal: &ordinal,
		RoundID:         "r_1",
		Timestamp:       time.Unix(4, 0).UTC(),
		TurnID:          "t_1",
		TurnKind:        TurnSpanExecution,
	}
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var back Turn
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, turn) {
		t.Fatalf("round trip = %+v, want %+v", back, turn)
	}
	// The public line projection re-marshals through sorted maps and relies on
	// the struct's field order matching the sort.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	sorted, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sorted, data) {
		t.Fatalf("field order differs from key order:\n got %s\nwant %s", data, sorted)
	}
}

func TestNoticePayloadsRoundTrip(t *testing.T) {
	for _, notice := range []NoticeInfo{
		{Kind: NoticeToolRepair, ToolRepair: &ToolRepairNotice{ToolName: "read_file", CallID: "c1", Changes: []string{"renamed path"}}},
		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "blocked", Reason: "stuck", Iterations: 3}},
		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4, MaxToolRoundsPerInput: 9}},
		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
		{Kind: NoticeApprovalDecision, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true, Tool: "write_file", Kind: "file_tool", DeniedPath: "/etc/hosts"}},
	} {
		data, err := json.Marshal(notice)
		if err != nil {
			t.Fatal(err)
		}
		var back NoticeInfo
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, notice) {
			t.Fatalf("round trip = %+v, want %+v", back, notice)
		}
	}
}

// Validate enforces NoticeInfo's own doc contract: exactly one payload,
// matching Kind. The read side (internal/apptranscript/notice.go) drops any
// mismatch silently, so the write side must refuse to record one.
func TestNoticeInfoValidate(t *testing.T) {
	valid := []NoticeInfo{
		{Kind: NoticeToolRepair, ToolRepair: &ToolRepairNotice{ToolName: "read_file"}},
		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}},
		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4}},
		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
		{Kind: NoticeApprovalDecision, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true}},
	}
	for _, notice := range valid {
		if err := notice.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", notice, err)
		}
	}
	invalid := []struct {
		name   string
		notice NoticeInfo
	}{
		{"zero payloads", NoticeInfo{Kind: NoticeGoalEnded}},
		{"two payloads", NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
		{"mismatched kind", NoticeInfo{Kind: NoticeGoalEnded, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
		{"approval without its payload", NoticeInfo{Kind: NoticeApprovalDecision}},
		{"approval payload under another kind", NoticeInfo{Kind: NoticeGoalEnded, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1"}}},
	}
	for _, tc := range invalid {
		if err := tc.notice.Validate(); err == nil {
			t.Errorf("%s: Validate(%+v) = nil, want an error", tc.name, tc.notice)
		}
	}
}

func TestLegacyTurnCarriesNoIdentity(t *testing.T) {
	data, err := json.Marshal(NewTurn(TurnUserInput, llm.User("hi")))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"format", "turn_id", "turn_kind", "round_id", "original_ordinal", "model", "completion", "communicate", "notice"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("a turn without identity marshals %q: %s", key, data)
		}
	}
}

func TestTranscriptOnlyKinds(t *testing.T) {
	for _, kind := range []TurnKind{TurnCompletion, TurnReopen, TurnCommunicate, TurnNotice} {
		if !kind.TranscriptOnly() {
			t.Errorf("%s is not transcript-only", kind)
		}
	}
	for _, kind := range []TurnKind{TurnUserInput, TurnSteering, TurnAssistant, TurnTool, TurnToolResults, TurnSystem, TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnFailure, TurnHookCompleted, TurnEnvironment, TurnNotesContext, TurnMemoryContext, TurnAttentionResolution} {
		if kind.TranscriptOnly() {
			t.Errorf("%s is transcript-only", kind)
		}
	}
}

// TestHarnessContextKindsAreClassified pins the predicate's true set and
// requires every TurnKind declared in the schema package to be on one side of
// it. A kind added to the schema without a decision fails here, so a new
// harness context kind cannot land while consumers still list the old set by
// hand.
func TestHarnessContextKindsAreClassified(t *testing.T) {
	want := map[TurnKind]bool{
		TurnEnvironment: true, TurnNotesContext: true, TurnMemoryContext: true,
		TurnUserInput: false, TurnSteering: false, TurnAssistant: false, TurnTool: false,
		TurnToolResults: false, TurnSystem: false, TurnCheckpoint: false, TurnSummary: false,
		TurnModelSwitch: false, TurnFailure: false, TurnHookCompleted: false,
		TurnAttentionResolution: false, TurnCompletion: false, TurnReopen: false,
		TurnCommunicate: false, TurnNotice: false,
	}
	for kind, classified := range want {
		if kind.HarnessContext() != classified {
			t.Errorf("HarnessContext(%s) = %t, want %t", kind, kind.HarnessContext(), classified)
		}
	}
	for _, kind := range declaredTurnKinds(t) {
		if _, ok := want[kind]; !ok {
			t.Errorf("TurnKind %s is declared in the schema package but not classified as harness context or not; add it here and to HarnessContext if the harness projects it as context", kind)
		}
	}
}

// declaredTurnKinds returns the value of every TurnKind constant declared in
// the schema package's non-test files, across every const block. Turn kinds
// declare their type and string value explicitly; a Turn-prefixed string
// constant without the type fails rather than escaping the classification.
func declaredTurnKinds(t *testing.T) []TurnKind {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read schema package: %v", err)
	}
	fset := token.NewFileSet()
	var kinds []TurnKind
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type == nil && len(vs.Names) == 1 && len(vs.Values) == 1 && strings.HasPrefix(vs.Names[0].Name, "Turn") {
					if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						t.Fatalf("%s: Turn-prefixed constant %s declares no TurnKind type; declare it as Name TurnKind = \"VALUE\"", fset.Position(vs.Pos()), vs.Names[0].Name)
					}
				}
				isTurnKind := false
				var valueExpr ast.Expr
				if ident, ok := vs.Type.(*ast.Ident); ok && ident.Name == "TurnKind" {
					isTurnKind = true
				} else if vs.Type == nil && len(vs.Values) == 1 {
					// A bare TurnKind conversion is still a TurnKind constant.
					if call, ok := vs.Values[0].(*ast.CallExpr); ok && len(call.Args) == 1 {
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "TurnKind" {
							isTurnKind = true
							valueExpr = call.Args[0]
						}
					}
				}
				if !isTurnKind {
					continue
				}
				if len(vs.Names) != 1 || len(vs.Values) != 1 {
					t.Fatalf("%s: TurnKind spec must declare exactly one name and one value", fset.Position(vs.Pos()))
				}
				if valueExpr == nil {
					valueExpr = vs.Values[0]
				}
				lit, ok := valueExpr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: TurnKind %s must declare its value as a string literal", fset.Position(vs.Pos()), vs.Names[0].Name)
				}
				kindValue, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: unquote %s: %v", fset.Position(vs.Pos()), vs.Names[0].Name, err)
				}
				kinds = append(kinds, TurnKind(kindValue))
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no TurnKind constants found in the schema package")
	}
	return kinds
}
