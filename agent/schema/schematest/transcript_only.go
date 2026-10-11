// Package schematest holds test fixtures for transcript entries that more than
// one package's tests need.
package schematest

import (
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TranscriptOnlySamples returns one entry of each transcript-only kind, each
// carrying identity the way a writer records it. None carries usage or tool
// parts: consumers that sum or match those must see nothing from them.
func TranscriptOnlySamples() []schema.Turn {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	sample := func(kind schema.TurnKind, turnID string) schema.Turn {
		return schema.Turn{Kind: kind, Message: llm.Message{Role: llm.RoleUser}, Timestamp: at, Format: schema.TurnFormatIdentity, TurnID: turnID, Model: "sample-model"}
	}
	completion := sample(schema.TurnCompletion, "t_sample_execution")
	completion.Completion = &schema.TurnCompletionInfo{Status: schema.TurnCompleted, CompletedAt: at, DurationMS: 42}
	reopen := sample(schema.TurnReopen, "turn_m9")
	// Shares completion's turn id rather than getting its own: recovery
	// reopens a crashed turn under its old id (agent/session_execution.go's
	// TurnReopen, transcript.Writer.BeginExecution(turnID, reopen=true)), so
	// a turn's entries can follow other turns' entries non-adjacently in the
	// file. InterleaveTranscriptOnly cycles through all seven samples
	// repeatedly across a long fixture, so this pairing revisits
	// "t_sample_execution" every cycle, exercising exactly that shape for
	// window pagination and the reference oracles that check it.
	communicate := sample(schema.TurnCommunicate, "t_sample_execution")
	communicate.Communicate = &schema.CommunicateInfo{CallID: "sample-call", EndTurn: true, Message: "a delivered message"}
	// Each notice gets its own turn id: real notices of different kinds come
	// from unrelated contexts (a tool repair, a goal ending, a turn limit, a
	// skill activation), so nothing ties them to the same synthetic turn.
	// Sharing one id here would let two non-adjacent samples revisit the same
	// turn out of physical order once InterleaveTranscriptOnly cycles back to
	// it, a case real writers don't produce.
	notice := func(id string, info schema.NoticeInfo) schema.Turn {
		turn := sample(schema.TurnNotice, id)
		turn.Notice = &info
		return turn
	}
	return []schema.Turn{
		completion,
		reopen,
		communicate,
		notice("t_sample_gap_repair", schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: "read_file", CallID: "sample-call", Changes: []string{"renamed argument"}}}),
		notice("t_sample_gap_goal", schema.NoticeInfo{Kind: schema.NoticeGoalEnded, GoalEnded: &schema.GoalEndedNotice{Status: "complete", Iterations: 1}}),
		notice("t_sample_gap_limit", schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxToolRoundsPerInput: 3}}),
		notice("t_sample_gap_skill", schema.NoticeInfo{Kind: schema.NoticeSkillActivated, SkillActivated: &schema.SkillActivatedNotice{Name: "sample-skill"}}),
		notice("t_sample_gap_approval", schema.NoticeInfo{Kind: schema.NoticeApprovalDecision, ApprovalDecision: &schema.ApprovalDecisionNotice{EscalationID: "sample-escalation", Approved: true, Tool: "write_file", Kind: "file_tool", DeniedPath: "/sample/path"}}),
	}
}

// InterleaveTranscriptOnly returns turns with a transcript-only sample before
// the first turn, between every adjacent pair (including between an
// assistant's tool calls and their results), and after the last, cycling
// through TranscriptOnlySamples. A consumer that skips the transcript-only
// kinds gives the same answer for the result as for turns.
func InterleaveTranscriptOnly(turns []schema.Turn) []schema.Turn {
	samples := TranscriptOnlySamples()
	out := make([]schema.Turn, 0, 2*len(turns)+1)
	next := 0
	add := func() {
		out = append(out, samples[next%len(samples)])
		next++
	}
	add()
	for _, turn := range turns {
		out = append(out, turn)
		add()
	}
	return out
}
