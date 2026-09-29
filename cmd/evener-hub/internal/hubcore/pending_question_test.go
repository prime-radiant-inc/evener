package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func testPendingQuestion() *appwire.PendingQuestion {
	return &appwire.PendingQuestion{Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2}
}

// fuzzScenarioStatusProber_KeepsThePendingQuestion: the probe keeps the listed
// root's first pending question beside its ask flag (S1b).
func fuzzScenarioStatusProber_KeepsThePendingQuestion(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_question",
		state:     appwire.ThreadStatusAwaiting,
		source:    wireProbeEnvelopeSource{question: testPendingQuestion()},
	})
	got := prober.Probe(entry)
	if !got.OK || !got.PendingAsk || !reflect.DeepEqual(got.PendingQuestion, testPendingQuestion()) {
		t.Fatalf("probe = %+v, want the pending question %+v", got, testPendingQuestion())
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves: a question
// answered and another asked between two probes leaves Status awaiting and
// PendingAsk set on both, and moves only the question the row names (S1b).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves(t *testing.T) {
	entry := func(question *appwire.PendingQuestion) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question}}
	}
	base := testPendingQuestion()
	text, options, count := testPendingQuestion(), testPendingQuestion(), testPendingQuestion()
	text.Question = "Which datastore for the ingest path?"
	options.Options = []string{"Drop them", "Keep them", "Ask me per tool"}
	count.Count = 3
	for name, moved := range map[string]*appwire.PendingQuestion{"text": text, "options": options, "count": count, "absent": nil} {
		if rosterFingerprint(entry(base)) == rosterFingerprint(entry(moved)) {
			t.Errorf("the roster fingerprint held when only the question's %s moved", name)
		}
	}
}

// fuzzScenarioRoster_EntriesOwnTheirPendingQuestion: an entry never aliases the
// probe's question, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirPendingQuestion(t *testing.T) {
	question := testPendingQuestion()
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, PendingAsk: true, PendingQuestion: question})
	question.Options[0] = "changed by the probe"
	if fromProbe.PendingQuestion.Options[0] != "Drop them" {
		t.Fatal("liveEntryFromProbe aliased the probe's option labels")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.PendingQuestion.Options[0] = "changed by the clone"
	if fromProbe.PendingQuestion.Options[0] != "Drop them" {
		t.Fatal("CloneLiveEntry aliased the entry's option labels")
	}
}

// fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion: an asking session's
// NeedsYou, Live and project rows and a meta-less live leaf name its first
// pending question from one closure, each row with its own copy; an entry that
// carries a question while its ask flag is clear names none on any row, as the
// approval detail does (S1b).
func fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ASKING", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01STALE", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01ASKING", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: testPendingQuestion()},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: testPendingQuestion()},
		{PID: 3, SessionID: "01STALE", Status: appwire.ThreadStatusAwaiting, PendingQuestion: testPendingQuestion()},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ASKING")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01NOMETA")
	var needsYou TreeNode
	for _, row := range tree.NeedsYou {
		if row.ID == "01ASKING" {
			needsYou = row
		}
	}
	if !inLive || !inProject || !inLeaf || needsYou.ID == "" {
		t.Fatalf("rows missing: live=%v project=%v leaf=%v needs-you=%+v", inLive, inProject, inLeaf, tree.NeedsYou)
	}
	rows := map[string]TreeNode{"NeedsYou": needsYou, "Live": liveRow, "project": projectRow, "live-only leaf": leaf}
	for name, row := range rows {
		if !reflect.DeepEqual(row.Question, testPendingQuestion()) {
			t.Fatalf("%s row question = %+v, want %+v", name, row.Question, testPendingQuestion())
		}
	}
	liveRow.Question.Options[0] = "changed on one row"
	if projectRow.Question.Options[0] != "Drop them" || needsYou.Question.Options[0] != "Drop them" {
		t.Fatal("two rows of one session share one question")
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01STALE" && row.Question != nil {
			t.Fatalf("a session whose ask flag is clear names the question %+v", row.Question)
		}
	}
	if _, _, stale, found := liveAndProjectRowsFor(tree, "01STALE"); !found || stale.Question != nil {
		t.Fatalf("stale session's project row = %+v (found %v), want no question", stale.Question, found)
	}
}
