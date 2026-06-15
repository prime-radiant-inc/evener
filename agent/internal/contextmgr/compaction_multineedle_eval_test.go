//go:build eval

package contextmgr

// Multi-needle compaction comparison: the conclusive test of the agent-authored
// note's value. The single-needle eval (compaction_comparison_eval_test.go) found
// the baseline summarizer never drops ONE salient fact, so the note had nothing to
// rescue. Here each case buries 6-8 DISTINCT facts interleaved through heavy bulk;
// a summarizer compressing to a handful of sections is forced to drop some — which
// is exactly where an agent-authored note (containing all the facts verbatim) earns
// its keep.
//
// Run: go test -tags eval ./agent/internal/contextmgr/ -run TestCompactionMultiNeedle -v -timeout 60m
//
// Arms, scored by COUNT-based retention (facts present / total facts):
//   A baseline:       summarizeWithLLMSteered(hist, 2, "")         -> summary only
//   C-summary steer:  summarizeWithLLMSteered(hist, 2, instruction) -> steered summary only
//   B note:           [NOTE all facts] + baseline summary           -> retention 1.0 by construction
//   C note+steer:     [NOTE all facts] + steered summary            -> retention 1.0 by construction
// The load-bearing numbers are A (how lossy is auto-compaction with many facts?) and
// C-summary (does steering alone make the summary keep more?). The note's value is
// 1 - A_retention (the fraction the baseline drops that a note would have preserved).
//
// The JUDGE is always gpt-5.5 (decoupled from the summarizer) so the quality signal
// stays discriminating even when the summarizer is weakened — the single-needle
// gpt-5.4-mini run showed a weak judge collapses every arm to 5/5.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// mnSummarizerModel is the summarizer under test. gpt-5.5 first (the realistic
// case); even a strong model should drop some of 6-8 interleaved facts.
const mnSummarizerModel = "gpt-5.5"

// mnJudgeModel is always strong/discriminating, independent of the summarizer.
const mnJudgeModel = "gpt-5.5"

var mnResultsDoc = "../../../docs/design/2026-06-14-compaction-multineedle-results-" + mnSummarizerModel + ".md"

// multiNeedleCase is one eval scenario: a history with several distinct facts
// interleaved through droppable bulk, plus the steering instruction that names
// what to keep.
type multiNeedleCase struct {
	name        string
	facts       []string // distinct must-survive substrings
	instruction string   // steering naming the facts to preserve
}

// buildHistory interleaves the case's fact lines with large bulk blocks so the
// facts are spread through the history (not conveniently clustered at the end).
func buildHistory(c multiNeedleCase) string {
	var b strings.Builder
	bulks := []string{
		repeat("go build ./... \ncc1 -O2 -fno-omit-frame-pointer ...\nDROPNOISE_build\n", 18),
		repeat("[2026-06-14T10:00:00Z] INFO: ci step completed DROPNOISE_ci\n", 22),
		repeat("=== RUN TestX\n--- PASS: TestX (0.00s)\nDROPNOISE_test\n", 20),
		repeat("grep: ./node_modules/x.js: binary file matches DROPNOISE_grep\n", 20),
		repeat("Step 14/52 : RUN apt-get install -y DROPNOISE_docker\n", 18),
		repeat("192.168.1.1 - - [14/Jun/2026] \"GET /h\" 200 42 DROPNOISE_http\n", 20),
		repeat("    0.01s 1.23% runtime.mallocgc DROPNOISE_pprof\n", 22),
		repeat("a1b2c3d Author: Bot DROPNOISE_git\n", 20),
	}
	// Interleave: bulk, fact, bulk, fact, ... ending with bulk so facts are buried.
	for i, f := range c.facts {
		b.WriteString(block(fmt.Sprintf("work output %d", i), bulks[i%len(bulks)]))
		b.WriteString(fact(f))
		b.WriteString("\n")
	}
	b.WriteString(block("final output", bulks[(len(c.facts))%len(bulks)]))
	return b.String()
}

func multiNeedleCases() []multiNeedleCase {
	return []multiNeedleCase{
		{
			name: "auth-subsystem-session",
			facts: []string{
				"JWT tokens use RS256 with a 1-hour expiry",
				"refresh endpoint is POST /api/v2/auth/refresh",
				"the signing key is rotated every 90 days via KMS key alias auth-signing-2026",
				"sessions table needs a NOT NULL column last_seen_at TIMESTAMPTZ",
				"the rate limiter caps refresh at 5 per minute per user_id",
				"decision: store refresh-token hashes (SHA-256), never raw tokens",
				"the failing test is TestRefresh_RotatesOnExpiry",
			},
			instruction: "Preserve the auth decisions: RS256/1-hour expiry, the /api/v2/auth/refresh endpoint, the 90-day KMS rotation (auth-signing-2026), the last_seen_at column, the 5/min rate limit, SHA-256 refresh-hash storage, and the failing test name. Drop the build/CI/test noise.",
		},
		{
			name: "payments-incident-debug",
			facts: []string{
				"root cause: double-charge when Stripe webhook retries within the 250ms idempotency window",
				"the idempotency key must be order_id plus attempt_number",
				"affected orders are between 2026-06-10 and 2026-06-12",
				"refund reconciliation runs via the nightly job recon-9912",
				"decision: widen the idempotency window to 5 seconds",
				"the broken commit is f9e8d7c in PaymentProcessor.Capture",
			},
			instruction: "Keep the incident facts: the 250ms-window double-charge root cause, the order_id+attempt_number idempotency key, the 2026-06-10..06-12 affected range, the recon-9912 nightly job, the 5-second window decision, and the broken commit f9e8d7c. Drop the log noise.",
		},
		{
			name: "db-migration-planning",
			facts: []string{
				"migration step 3 renames column user_id to account_uuid in the orders table",
				"add index CONCURRENTLY idx_orders_account_status on orders(account_uuid, status)",
				"the cutover window is Sunday 02:00-04:00 UTC",
				"rollback uses the pre-migration snapshot snap-44219",
				"decision: dual-write to both columns for one release before dropping user_id",
				"Postgres was chosen over SQLite because of concurrent-writer throughput",
			},
			instruction: "Preserve the migration plan: step-3 user_id->account_uuid rename, the idx_orders_account_status concurrent index, the Sunday 02:00-04:00 UTC cutover, snapshot snap-44219 for rollback, the dual-write-for-one-release decision, and the Postgres-over-SQLite rationale. Drop the noise.",
		},
		{
			name: "perf-optimization-session",
			facts: []string{
				"bottleneck: 78% of CPU is in encoding/json.Marshal",
				"decision: switch the hot path to jsoniter",
				"batching 100 writes per transaction cut latency from 120ms to 8ms",
				"the gRPC keepalive (10s) must align with the LB idle timeout (5s)",
				"cache shard count must stay <= 16 to avoid the CacheManager.Evict race",
				"the benchmark to watch is BenchmarkOrderLookup (regressed in PR #4421)",
			},
			instruction: "Keep the performance findings: the json.Marshal 78% bottleneck, the jsoniter switch, the 100-writes-per-txn 120ms->8ms win, the 10s/5s keepalive-vs-LB alignment, the <=16 shard CacheManager.Evict constraint, and BenchmarkOrderLookup / PR #4421. Drop the profiler/bench noise.",
		},
		{
			name: "api-contract-design",
			facts: []string{
				"the /v3/orders endpoint must return X-Request-ID in every response header",
				"pagination uses opaque cursors, not offsets, max page size 200",
				"errors follow RFC 7807 problem+json",
				"the deprecation sunset for /v2/orders is 2026-12-01",
				"all timestamps are RFC3339 UTC with millisecond precision",
				"decision: idempotency-key header required on POST /v3/orders",
			},
			instruction: "Preserve the API contract: X-Request-ID on /v3/orders, opaque cursor pagination (max 200), RFC 7807 errors, the 2026-12-01 /v2 sunset, RFC3339 millisecond UTC timestamps, and the required idempotency-key on POST /v3/orders. Drop the noise.",
		},
		{
			name: "infra-rollout-plan",
			facts: []string{
				"canary 5% traffic to v2 on Monday, full cutover Friday if error rate < 0.1%",
				"staging uses t3.medium, production uses t3.large only",
				"the release image tag is api-server:v2.14.0-rc3",
				"feature flag rollout_v2 gates the new path",
				"DATABASE_URL must include ?pool_size=20",
				"the on-call runbook is at wiki/runbooks/v2-rollout",
			},
			instruction: "Keep the rollout plan: Monday 5% canary / Friday cutover at <0.1% errors, t3.medium staging vs t3.large prod, image tag api-server:v2.14.0-rc3, the rollout_v2 flag, DATABASE_URL ?pool_size=20, and the v2-rollout runbook. Drop the noise.",
		},
		{
			name: "search-indexing-design",
			facts: []string{
				"the index is rebuilt nightly at 03:00 via job index-rebuild-7711",
				"documents are sharded by tenant_id into 32 shards",
				"decision: use BM25 ranking, not the older TF-IDF path",
				"the ingestion lag SLO is under 60 seconds p99",
				"deleted docs are tombstoned for 24h before physical removal",
				"the query timeout is 800ms with a 3-shard fan-out cap",
			},
			instruction: "Preserve the search design: the nightly 03:00 index-rebuild-7711 job, 32-shard tenant_id sharding, the BM25-over-TF-IDF decision, the 60s-p99 ingestion-lag SLO, the 24h tombstone, and the 800ms / 3-shard query limits. Drop the noise.",
		},
		{
			name: "security-review-findings",
			facts: []string{
				"finding: the upload handler allows path traversal via the filename field",
				"fix: canonicalize and confine uploads under /var/data/uploads",
				"secrets must move from env vars to the vault path secret/data/api/prod",
				"TLS minimum version is bumped to 1.3",
				"the audit log must capture actor_id, action, and resource for every mutation",
				"CVE-2026-31337 in the image-parsing dep requires upgrade to v3.2.1",
			},
			instruction: "Keep the security findings: the upload path-traversal via filename, the /var/data/uploads confinement fix, the secret/data/api/prod vault migration, TLS 1.3 minimum, the actor_id/action/resource audit requirement, and CVE-2026-31337 -> v3.2.1. Drop the noise.",
		},
		{
			name: "observability-rollout",
			facts: []string{
				"traces sample at 10% baseline, 100% for error spans",
				"the SLO burn-rate alert fires at 2% over 1h",
				"metrics are scraped every 15s, retained 13 months",
				"decision: adopt OpenTelemetry, deprecate the custom statsd path",
				"the dashboard of record is grafana board uid svc-health-42",
				"log lines must include trace_id for correlation",
			},
			instruction: "Preserve the observability plan: 10% baseline / 100% error-span trace sampling, the 2%-over-1h burn-rate alert, 15s scrape / 13-month retention, the OpenTelemetry-over-statsd decision, grafana uid svc-health-42, and the trace_id-in-logs requirement. Drop the noise.",
		},
		{
			name: "data-pipeline-decisions",
			facts: []string{
				"events are partitioned by event_date and bucketed by user_id mod 64",
				"the backfill window is 2026-01-01 forward, replayed from topic events-v3",
				"decision: exactly-once via the transactional outbox pattern",
				"the schema registry enforces backward compatibility only",
				"late events beyond a 2-hour watermark are routed to the dead-letter topic dlq-events",
				"the nightly aggregate job is agg-roll-5530",
			},
			instruction: "Keep the pipeline decisions: event_date partition + user_id mod 64 bucketing, the 2026-01-01 backfill from events-v3, the transactional-outbox exactly-once choice, backward-only schema compatibility, the 2-hour watermark to dlq-events, and the agg-roll-5530 job. Drop the noise.",
		},
	}
}

// oauthClientAndCfg builds the OAuth-backed client + provider config once,
// mirroring newOAuthManager but reusable for multiple profiles (summarizer +
// judge at different models).
func oauthClientAndCfg(t *testing.T) (*llm.Client, providercfg.Config) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(oauthStateHome, "serf", "auth", "openai.json")); err != nil {
		t.Skipf("no OAuth record at %s/serf/auth/openai.json: %v", oauthStateHome, err)
	}
	t.Setenv("XDG_STATE_HOME", oauthStateHome)
	cfg, exists, err := providercfg.LoadFile(oauthProvidersConfig)
	if err != nil {
		t.Fatalf("load providers.toml: %v", err)
	}
	if !exists {
		t.Skipf("providers.toml not found at %s", oauthProvidersConfig)
	}
	client, errs, err := llm.NewFromAvailableProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromAvailableProviders: %v (errs: %v)", err, errs)
	}
	return client, cfg
}

func managerForModel(t *testing.T, client *llm.Client, cfg providercfg.Config, model string) *Manager {
	t.Helper()
	prof, err := provider.ResolveProfileFromConfig(cfg, "openai/"+model)
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig openai/%s: %v", model, err)
	}
	return NewManager(prof, client)
}

// retention returns the fraction of the case's facts present in out.
func retention(out string, facts []string) float64 {
	if len(facts) == 0 {
		return 1
	}
	kept := 0
	for _, f := range facts {
		if strings.Contains(out, f) {
			kept++
		}
	}
	return float64(kept) / float64(len(facts))
}

type mnRow struct {
	name                                   string
	aRet, cSummaryRet                      float64 // baseline summary, steered summary (no note)
	judgeA, judgeB, judgeC                 int
}

func TestCompactionMultiNeedle(t *testing.T) {
	client, cfg := oauthClientAndCfg(t)
	summ := managerForModel(t, client, cfg, mnSummarizerModel)
	judge := managerForModel(t, client, cfg, mnJudgeModel)

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	cases := multiNeedleCases()
	rows := make([]mnRow, 0, len(cases))

	noteOf := func(facts []string) string {
		return "[NOTE TO SELF]\n" + strings.Join(facts, "\n") + "\n[END NOTE TO SELF]\n"
	}

	for _, c := range cases {
		hist := turnsFromText(t, buildHistory(c))
		note := noteOf(c.facts)

		aOut, err := summ.summarizeWithLLMSteered(ctx, hist, 2, "")
		if err != nil {
			t.Fatalf("case %q arm A: %v", c.name, err)
		}
		aSummary := summaryText(aOut)

		cOut, err := summ.summarizeWithLLMSteered(ctx, hist, 2, c.instruction)
		if err != nil {
			t.Fatalf("case %q arm C-summary: %v", c.name, err)
		}
		cSummary := summaryText(cOut)

		aRet := retention(aSummary, c.facts)
		cSummaryRet := retention(cSummary, c.facts)
		// B and C combined include the note (all facts verbatim) -> retention 1.0.

		taskCtx := "The agent's history contained " + fmt.Sprintf("%d", len(c.facts)) +
			" distinct critical facts that must survive compaction:\n  - " + strings.Join(c.facts, "\n  - ")
		jA := judgeQuality(t, ctx, judge, taskCtx, aSummary)
		jB := judgeQuality(t, ctx, judge, taskCtx, note+aSummary)
		jC := judgeQuality(t, ctx, judge, taskCtx, note+cSummary)

		rows = append(rows, mnRow{c.name, aRet, cSummaryRet, jA, jB, jC})
		t.Logf("case %-28s baseline-retention=%.2f steered-summary-retention=%.2f | judge A=%d B=%d C=%d (facts=%d)",
			c.name, aRet, cSummaryRet, jA, jB, jC, len(c.facts))
	}

	var sumA, sumCS float64
	var jsA, jsB, jsC, jn int
	for _, r := range rows {
		sumA += r.aRet
		sumCS += r.cSummaryRet
		jsA += r.judgeA
		jsB += r.judgeB
		jsC += r.judgeC
		jn++
	}
	n := float64(len(rows))
	meanA, meanCS := sumA/n, sumCS/n
	jmean := func(s int) float64 { return float64(s) / float64(jn) }

	t.Logf("=== MULTI-NEEDLE AGGREGATE (%d cases, summarizer=%s, judge=%s) ===", len(rows), mnSummarizerModel, mnJudgeModel)
	t.Logf("baseline (A) fact retention:        %.2f", meanA)
	t.Logf("steered-summary (no note) retention: %.2f", meanCS)
	t.Logf("note rescue value (1 - baseline):    %.2f", 1-meanA)
	t.Logf("steering's summary lift (cs - a):    %+.2f", meanCS-meanA)
	t.Logf("mean judge: A(baseline)=%.2f  B(note)=%.2f  C(note+steer)=%.2f", jmean(jsA), jmean(jsB), jmean(jsC))

	writeMultiNeedleDoc(t, rows, meanA, meanCS, jmean(jsA), jmean(jsB), jmean(jsC))
}

func judgeQuality(t *testing.T, ctx context.Context, judge *Manager, taskCtx, handoff string) int {
	t.Helper()
	n, _, err := judge.judgeOutput(ctx, taskCtx, handoff)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	return n
}

func writeMultiNeedleDoc(t *testing.T, rows []mnRow, meanA, meanCS, jA, jB, jC float64) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# Multi-needle compaction comparison: where the note earns its keep\n\n")
	fmt.Fprintf(&b, "_Generated by `TestCompactionMultiNeedle` (`//go:build eval`) on %s — summarizer `%s`, judge `%s`, real OAuth OpenAI, no mocks._\n\n",
		time.Now().Format("2006-01-02"), mnSummarizerModel, mnJudgeModel)
	fmt.Fprintf(&b, "Each of the %d cases buries 6-8 distinct facts through heavy interleaved bulk. Retention is **count-based** (facts present / total).\n\n", len(rows))
	fmt.Fprintf(&b, "## Aggregate\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| **Baseline (A) fact retention** | **%.2f** |\n", meanA)
	fmt.Fprintf(&b, "| Steered-summary (no note) retention | %.2f |\n", meanCS)
	fmt.Fprintf(&b, "| **Note rescue value (1 − baseline)** | **%.2f** |\n", 1-meanA)
	fmt.Fprintf(&b, "| Steering's summary lift (steered − baseline) | %+.2f |\n", meanCS-meanA)
	fmt.Fprintf(&b, "| Mean judge — A baseline | %.2f |\n", jA)
	fmt.Fprintf(&b, "| Mean judge — B note | %.2f |\n", jB)
	fmt.Fprintf(&b, "| Mean judge — C note+steer | %.2f |\n\n", jC)
	fmt.Fprintf(&b, "**Reading:** with the note (arms B/C) every fact survives by construction (retention 1.0). "+
		"The baseline (A) retention above is the fraction auto-compaction keeps on its own; **1 − that is exactly the fraction a note would have rescued.**\n\n")
	fmt.Fprintf(&b, "## Per-case\n\n| Case | baseline retention | steered-summary retention | judge A/B/C |\n|---|---|---|---|\n")
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %.2f | %.2f | %d/%d/%d |\n", r.name, r.aRet, r.cSummaryRet, r.judgeA, r.judgeB, r.judgeC)
	}
	if err := os.WriteFile(mnResultsDoc, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write results doc: %v", err)
	}
	abs, _ := filepath.Abs(mnResultsDoc)
	t.Logf("wrote results doc: %s", abs)
}
