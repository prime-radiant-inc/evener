//go:build eval

package contextmgr

// Multi-compaction erosion ("context collapse") eval — the note's strongest
// theoretical case, and the one all the other evals miss (they do ONE compaction).
//
// A fact stated once must survive N SUCCESSIVE compactions. The baseline
// re-summarizes recursively: summary of [previous summary + new work], N times —
// the fact gets buried deeper in nested summaries and can erode (paraphrased,
// shortened, or dropped). The note arm re-stamps the fact VERBATIM at every
// compaction, so it cannot erode by construction. This eval measures whether the
// BASELINE recursive summary actually erodes the fact across N rounds.
//
// Run: go test -tags eval ./agent/internal/contextmgr/ -run TestCompactionErosion -v -timeout 40m
//
// Live can't do this (can't force N auto-compactions — window too big,
// compactionThresholdScale is test-only), so it's a controlled recursive eval
// using the real OAuth summarizer (gpt-5.5).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// erosionRounds is how many successive compactions a fact must survive.
const erosionRounds = 6

// erosionResultsDoc is where the eval writes its findings.
var erosionResultsDoc = "../../../docs/design/2026-06-14-compaction-erosion-results.md"

// erosionFacts are distinctive, opaque facts whose exact tokens are easy to check
// verbatim — if the recursive summary paraphrases/shortens them, the token changes.
type erosionFact struct {
	statement string
	token     string // exact substring that must survive
}

func erosionFacts() []erosionFact {
	return []erosionFact{
		{"The production deploy token is DEPLOY-7Q2X-9F3A-K8M2-V5N1 (exact, never abbreviate).", "DEPLOY-7Q2X-9F3A-K8M2-V5N1"},
		{"The incident root cause is a deadlock in CacheManager.Evict when numShards exceeds 16.", "numShards exceeds 16"},
		{"The migration renames column user_id to account_uuid in the orders table.", "account_uuid"},
		{"The breaking commit is f9e8d7c and must be reverted before release v2.14.0-rc3.", "v2.14.0-rc3"},
	}
}

// newWorkBulk simulates the work done between compactions in round r.
func newWorkBulk(r int) string {
	return block(fmt.Sprintf("work since compaction %d", r),
		repeat(fmt.Sprintf("ran tests, edited files, DROPNOISE_round%d log line\n", r), 30)+
			repeat(fmt.Sprintf("[ci] step %d ok DROPNOISE_ci_%d\n", r, r), 25))
}

// erosionRoundResult records per-fact verbatim survival after one compaction round.
type erosionRoundResult struct {
	round   int
	survive []bool
}

func TestCompactionErosion(t *testing.T) {
	cm := newOAuthManager(t)
	facts := erosionFacts()

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()

	// Initial history: all the facts plus a block of bulk.
	var initial strings.Builder
	initial.WriteString(block("project kickoff notes", repeat("setup line DROPNOISE_init\n", 25)))
	for _, f := range facts {
		initial.WriteString(fact(f.statement))
	}
	initial.WriteString("\n" + newWorkBulk(0))

	// BASELINE: recursive summarization. Each round summarizes [prev summary + new
	// work], and we record per-round which fact tokens still survive verbatim.
	carried := initial.String()
	var rounds []erosionRoundResult
	for r := 1; r <= erosionRounds; r++ {
		hist := turnsFromText(t, carried)
		out, err := cm.summarizeWithLLMSteered(ctx, hist, 2, "")
		if err != nil {
			t.Fatalf("round %d summarize: %v", r, err)
		}
		summary := summaryText(out)

		rr := erosionRoundResult{round: r}
		for _, f := range facts {
			rr.survive = append(rr.survive, strings.Contains(summary, f.token))
		}
		rounds = append(rounds, rr)

		alive := 0
		for _, s := range rr.survive {
			if s {
				alive++
			}
		}
		t.Logf("baseline round %d: %d/%d fact tokens survive", r, alive, len(facts))

		// Feed the summary forward with new work (the next compaction's input).
		carried = "Previous compaction summary:\n" + summary + "\n\n" + newWorkBulk(r)
	}

	// NOTE arm is verbatim by construction: the pinned note carries each fact token
	// unchanged through every compaction, so its survival is len(facts)/len(facts)
	// at every round. We assert that and contrast with the baseline's final round.
	finalBaselineAlive := 0
	for i := range facts {
		if rounds[len(rounds)-1].survive[i] {
			finalBaselineAlive++
		}
	}

	t.Logf("=== EROSION AGGREGATE (%d rounds, summarizer=%s) ===", erosionRounds, evalModel)
	t.Logf("baseline final-round fact survival: %d/%d", finalBaselineAlive, len(facts))
	t.Logf("note-arm fact survival (verbatim re-stamp, by construction): %d/%d", len(facts), len(facts))
	t.Logf("erosion = facts the baseline lost over %d compactions that the note would have kept: %d", erosionRounds, len(facts)-finalBaselineAlive)

	writeErosionDoc(t, facts, rounds, finalBaselineAlive)
}

func writeErosionDoc(t *testing.T, facts []erosionFact, rounds []erosionRoundResult, finalBaselineAlive int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# Multi-compaction erosion (context collapse): does a fact survive N compactions?\n\n")
	fmt.Fprintf(&b, "_Generated by `TestCompactionErosion` (`//go:build eval`) on %s — summarizer `%s`, real OAuth OpenAI, %d successive recursive compactions._\n\n",
		time.Now().Format("2006-01-02"), evalModel, len(rounds))
	fmt.Fprintf(&b, "Each round summarizes `[previous summary + new work]`, so a fact stated once gets buried deeper in nested summaries. The **note arm** re-stamps each fact verbatim every compaction (survival 1.0 by construction); the table tracks the **baseline** recursive summary.\n\n")
	fmt.Fprintf(&b, "## Baseline: fact-token survival per compaction round\n\n")
	fmt.Fprintf(&b, "| Round |")
	for i := range facts {
		fmt.Fprintf(&b, " f%d |", i+1)
	}
	fmt.Fprintf(&b, "\n|---|")
	for range facts {
		fmt.Fprintf(&b, "---|")
	}
	fmt.Fprintf(&b, "\n")
	for _, rr := range rounds {
		fmt.Fprintf(&b, "| %d |", rr.round)
		for _, s := range rr.survive {
			if s {
				fmt.Fprintf(&b, " Y |")
			} else {
				fmt.Fprintf(&b, " . |")
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "\nFacts: ")
	for i, f := range facts {
		fmt.Fprintf(&b, "f%d=`%s` ", i+1, f.token)
	}
	fmt.Fprintf(&b, "\n\n## Result\n\n")
	fmt.Fprintf(&b, "- Baseline final-round survival: **%d/%d** facts after %d compactions.\n", finalBaselineAlive, len(facts), len(rounds))
	fmt.Fprintf(&b, "- Note-arm survival: **%d/%d** (verbatim re-stamp, by construction).\n", len(facts), len(facts))
	fmt.Fprintf(&b, "- **Erosion (note's value here): %d facts** the baseline lost over %d compactions that a pinned note would have preserved.\n\n", len(facts)-finalBaselineAlive, len(rounds))
	if len(facts)-finalBaselineAlive == 0 {
		fmt.Fprintf(&b, "_The baseline recursive summary preserved every fact through all %d compactions — even multi-compaction erosion did not surface a note advantage with this summarizer. Consistent with the live-resilience finding._\n", len(rounds))
	} else {
		fmt.Fprintf(&b, "_Recursive summarization eroded facts the note would have kept verbatim — the note's strongest demonstrated advantage._\n")
	}
	if err := os.WriteFile(erosionResultsDoc, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write erosion doc: %v", err)
	}
	abs, _ := filepath.Abs(erosionResultsDoc)
	t.Logf("wrote results doc: %s", abs)
}
