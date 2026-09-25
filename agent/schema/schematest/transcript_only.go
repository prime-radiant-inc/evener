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
	communicate := sample(schema.TurnCommunicate, "t_sample_execution")
	communicate.Communicate = &schema.CommunicateInfo{CallID: "sample-call", EndTurn: true, Message: "a delivered message"}
	notice := func(info schema.NoticeInfo) schema.Turn {
		turn := sample(schema.TurnNotice, "t_sample_gap")
		turn.Notice = &info
		return turn
	}
	return []schema.Turn{
		completion,
		reopen,
		communicate,
		notice(schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: "read_file", CallID: "sample-call", Changes: []string{"renamed argument"}}}),
		notice(schema.NoticeInfo{Kind: schema.NoticeGoalEnded, GoalEnded: &schema.GoalEndedNotice{Status: "complete", Iterations: 1}}),
		notice(schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxToolRoundsPerInput: 3}}),
		notice(schema.NoticeInfo{Kind: schema.NoticeSkillActivated, SkillActivated: &schema.SkillActivatedNotice{Name: "sample-skill"}}),
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
