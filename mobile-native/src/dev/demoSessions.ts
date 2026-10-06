// The demo fleet's sessions as the Session screen reads them (thread/read),
// so Appendix A's Session frames (7-14 and 13a of
// docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md) can be
// seen and screenshotted without a real hub. demoFleet.ts serves the Board's
// rows; this file serves each of those rows' own thread, on the same ref.
//
// Every fleet session gets a thread, so the Session title's swipe always
// lands on a real neighbor. The frames' sessions carry content transcribed
// from the prototype's transcripts, asks and approvals
// (docs/design/mobile/redesign/prototype/data.js, `T`, `ASKS` and
// `APPROVALS`), turned into wire items: each prototype step becomes the tool
// call a real session makes for it. Every other session gets data.js's
// `generic` transcript. The frames:
// - 7, 13 and 14: s-pr2138, working, with subagents, tasks, a goal, notes,
//   links and one queued message;
// - 8: s-audit, asking two questions;
// - 9: s-mirror, waiting on an approval to write outside its workspace;
// - 10: s-tasklist, working with one queued message;
// - 11: s-retry, whose last turn failed on a sign-in error;
// - 12: s-jobdisp, with an edit and a 60-line command output to open at Tools;
// - 13a: s-pr2138's notes, and s-roster, shut down with read-only notes.
// s-tools replays the recorded wire corpora: one step of every tool family.
// s-stumble queues five messages, more than the Session shows inline, so its
// "2 more queued" opens the Queue sheet.
import type {
	EmptyResponse,
	EvenerDelegateInfo,
	GoalState,
	NotesHumanSetParams,
	NotesHumanSetResponse,
	PendingQuestion,
	QueueState,
	SandboxEscalationRequested,
	SandboxEscalationResolveParams,
	SessionURL,
	TaskAggregate,
	Thread,
	ThreadItem,
	Turn,
	ThreadCapabilities,
	TurnError,
	UrlsRemoveParams,
	UrlsRemoveResponse,
} from "@evener/appwire-client";
import { SHUT_DOWN_STATUSES, WireError } from "@evener/appwire-client";
import {
	demoSessionId,
	enabledPluginNames,
	type FleetSession,
	fleetSessions,
	hostSessionRef,
	type ProtoState,
	type RawSubagent,
} from "./demoFleet";
import { NOTE_STEER_CLEARED, NOTE_STEER_PREFIX } from "../projectedRows";
import { demoRunStartedAt } from "./demoSubagents";
import { recordedToolCwd, recordedToolFamilies } from "./demoToolFamilies";

const ALL_EFFORTS = ["low", "medium", "high", "xhigh", "max"];

// One prototype step as the tool call a real session makes for it.
interface Step {
	tool: string;
	intent: string;
	args: Record<string, unknown>;
	seconds: number;
	output?: string;
	failed?: { error: string; exitCode: number };
	// The step still running: the status tray's line.
	running?: true;
}

interface Question {
	header: string;
	question: string;
	why: string;
	multi_select: boolean;
	options: { label: string; detail: string; recommended?: true }[];
}

type Entry =
	| { user: string }
	| { steer: string }
	| { agent: string }
	| { step: Step }
	// A subagent the session started, by its id in the session's subagent tree.
	| { subagent: string }
	| { thinking: true }
	| { ask: Question[] }
	// A daemon steering item of kind "notification": a job or delegate
	// notification as the daemon delivers it.
	| { notice: string }
	// An item recorded from a real run (demoToolFamilies.ts), served as it
	// was, with its own id, on the demo turn and clock.
	| { recorded: ThreadItem }
	// A daemon warning, as the hub's overlay announces one
	// (internal/appoverlay/notices.go warningAnnouncement): a systemMessage
	// with eventKind "warning" and its title and hint on raw.warning.
	| { warning: { text: string; title: string; hint: string } };

// What a frame's session carries beyond its fleet row.
interface SessionContent {
	entries?: Entry[];
	// The directory the session sits in, when it isn't its project's.
	cwd?: string;
	error?: TurnError;
	model?: string;
	effort?: string;
	queued?: string[];
	tasks?: TaskAggregate;
	goal?: GoalState;
	humanNote?: string;
	agentNote?: string;
	links?: Omit<SessionURL, "addedBy">[];
	escalation?: Pick<SandboxEscalationRequested, "tool" | "kind" | "mode" | "deniedPath" | "partiallyRan">;
	usage?: Usage;
}

// A session's usage for the Session sheet: tokens, the estimated cost, and
// how much of its context window it holds.
interface Usage {
	inputTokens: number;
	outputTokens: number;
	cacheReadTokens: number;
	cost: string;
	contextUsed: number;
	contextWindow: number;
}

// data.js's `base` usage, which every session it doesn't override shares.
const BASE_USAGE: Usage = {
	inputTokens: 38_200_000,
	outputTokens: 1_100_000,
	cacheReadTokens: 29_400_000,
	cost: "~$11.40",
	contextUsed: 96_000,
	contextWindow: 256_000,
};

const DEFAULT_MODEL = "lunaroute/deepseek-4.1-flash";
const DEFAULT_EFFORT = "xhigh";

// data.js's subagent lines for the failures a frame shows; any other failed
// subagent reads as data.js's genericSubs failure.
const FAILURE_REASONS: Record<string, string> = {
	"g-settle": "Failed: go test exited 1 (3 times)",
	"g-repro": "Failed: could not reproduce in 200 runs",
	"r-3": "Failed: provider sign-in expired",
};
const GENERIC_FAILURE = "Failed: tests still failing after 3 attempts";

const shell = (intent: string, command: string, seconds: number, output?: string): Step => ({
	tool: "shell",
	intent,
	args: { command },
	seconds,
	...(output === undefined ? {} : { output }),
});

// A test run's output, `lines` lines long: enough for "Show all N lines".
function testOutput(lines: number): string {
	const passes = Array.from({ length: lines - 2 }, (_, index) => `✓ JobRow case ${index + 1} (${(index % 7) + 2} ms)`);
	return [...passes, "", `Tests  ${lines - 2} passed (${lines - 2})`].join("\n");
}

const CONTENT: Record<string, SessionContent> = {
	"s-pr2138": {
		entries: [
			{ user: "PR 2138 has 6 failing tests on CI. Get it clean without skipping anything." },
			{
				agent:
					"I'll pull the failing runs, group the failures by cause, and give each cause its own subagent. I'll keep the goal as *green without skipped tests*.",
			},
			{
				step: shell(
					"Read the CI failures",
					"gh run view 34717544502 --log-failed",
					40,
					"--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.41s)\n--- FAIL: TestFoldPublicationMarkersLandInOrder (0.12s)\n--- FAIL: TestQueueRetirementCancelsQueued (0.08s)\nFAIL\tgithub.com/prime-radiant-inc/evener/agent\t41.2s",
				),
			},
			{
				step: {
					...shell(
						"Ran the agent tests",
						"go test ./agent/... -run 'Retirement|Fold' -count=5",
						190,
						"--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.44s)\n    retirement_test.go:212: settle finished before drain: got state idle, want draining\nFAIL\nexit status 1",
					),
					failed: { error: "exit status 1", exitCode: 1 },
				},
			},
			{
				step: {
					tool: "edit_file",
					intent: "Edited the settle pass",
					args: {
						file_path: "agent/retirement.go",
						old_string: "func (t *tree) settle(ctx context.Context) error {\n\tt.mu.Lock()\n\tdefer t.mu.Unlock()",
						new_string:
							'func (t *tree) settle(ctx context.Context) error {\n\tif err := t.drain.wait(ctx); err != nil {\n\t\treturn fmt.Errorf("settle: wait for drain: %w", err)\n\t}\n\tt.mu.Lock()\n\tdefer t.mu.Unlock()',
					},
					seconds: 50,
				},
			},
			{
				agent:
					"The flaky tests share one cause: a race between the **tree settle pass** and the **retirement drain**. Both take the tree lock, and settle can finish before drain has seen the pending work.\n\nI've written the fix up as a plan in `docs/superpowers/plans/2026-09-25-settle-race.md` and I'm splitting it across subagents: one fixes the race, others run every affected package under `-race` to prove it.",
			},
			{ subagent: "g-settle" },
			{ subagent: "g-run-0" },
			{ steer: "Also make sure the race test runs under -race on Linux, not just macOS." },
			{ agent: "Good call. I added a Linux `-race` run to the plan and gave it its own subagent on magic-kingdom." },
			{
				step: {
					tool: "edit_file",
					intent: "Added the Linux -race run to the plan",
					args: {
						file_path: "docs/superpowers/plans/2026-09-25-settle-race.md",
						old_string: "- Run the flaky tests 200 times on macOS.",
						new_string: "- Run the flaky tests 200 times on macOS.\n- Run them under -race on Linux (magic-kingdom).",
					},
					seconds: 20,
				},
			},
			{
				step: {
					tool: "job_watch",
					intent: "Waiting on subagents",
					args: { job_ids: ["g-run-0"] },
					seconds: 0,
					running: true,
				},
			},
		],
		queued: ["When CI is green, post a summary on the PR."],
		tasks: { total: 7, done: 3, remaining: 4, current: { id: 4, description: "Fix the settle/drain race" } },
		goal: { objective: "PR 2138 green on CI without skipped tests", status: "active", iterations: 3 },
		usage: {
			inputTokens: 46_100_000,
			outputTokens: 1_900_000,
			cacheReadTokens: 38_800_000,
			cost: "~$19.60",
			contextUsed: 38_000,
			contextWindow: 256_000,
		},
		humanNote: "Don't skip or quarantine tests. Fix causes.",
		agentNote: "Race is in retirement drain vs tree settle; see plan.",
		links: [
			{ id: "u-2138", url: "https://github.com/prime-radiant-inc/evener/pull/2138", label: "PR #2138" },
			{ id: "u-checks", url: "https://github.com/prime-radiant-inc/evener/pull/2138/checks", label: "CI checks" },
			{
				id: "u-splan",
				url: "file:///home/jesse/git/prime-radiant-inc/evener/docs/superpowers/plans/2026-09-25-settle-race.md",
				label: "Settle race plan",
			},
		],
	},
	"s-audit": {
		model: "lunaroute/glm-5.3-vision",
		entries: [
			{
				user: "Audit every tool description in agent/internal/tool/definitions.go for options the tool doesn't actually accept. List them and fix the descriptions.",
			},
			{
				agent:
					"I'll read all 61 tool definitions and check each description against the tool's real argument schema. I'm splitting the tools across subagents so this goes quickly.",
			},
			{
				step: {
					tool: "read_file",
					intent: "Read the tool definitions",
					args: { file_path: "agent/internal/tool/definitions.go" },
					seconds: 4,
					output: "1,412 lines, 61 tools",
				},
			},
			{
				step: {
					tool: "grep",
					intent: "Found every argument schema",
					args: { pattern: "Parameters:", path: "agent/internal/tool" },
					seconds: 3,
				},
			},
			{ subagent: "s-audit-g0" },
			{ subagent: "s-audit-g1" },
			{ subagent: "s-audit-g2" },
			{
				agent:
					"## What I found\n\n14 descriptions mention options their tools don't accept. The worst three:\n\n| Tool | Implied option | Accepted |\n|---|---|---|\n| `read_file` | `--force` | no |\n| `job_watch` | `timeout_ms` | no |\n| `delegate` | `priority` | no |\n\nThere are two reasonable fixes and they change behavior differently, so I'd like your call.",
			},
			{
				ask: [
					{
						header: "Implied options",
						question: "Keep or drop the implied options?",
						why: "Fourteen descriptions mention flags the tools don't accept, like --force on read_file. Models try them and fail.",
						multi_select: false,
						options: [
							{ label: "Drop them", detail: "Remove the implied options from all 14 descriptions", recommended: true },
							{ label: "Keep them and add the flags", detail: "Implement the 9 missing flags; about 400 lines" },
							{ label: "Ask me per tool", detail: "I'll list each one for a yes or no" },
						],
					},
					{
						header: "Next audit",
						question: "Which tool groups should I audit next?",
						why: "I can check the rest the same way while you review.",
						multi_select: true,
						options: [
							{ label: "Job tools", detail: "job_watch, job_status, job_list, job_stop", recommended: true },
							{ label: "Delegate tools", detail: "delegate, delegate_send" },
							{ label: "Search tools", detail: "grep, glob, search" },
							{ label: "Notes tools", detail: "notes and urls" },
						],
					},
				],
			},
		],
	},
	"s-mirror": {
		effort: "high",
		entries: [
			{ user: "Mirror the docs site into ~/sites/docs so I can browse it offline." },
			{
				agent:
					"I'll fetch the site map and write each page under `~/sites/docs`. That folder is outside this session's workspace, so I'll need your approval to write there.",
			},
			{ step: shell("Fetched the site map", "curl -s https://docs.prime-radiant.com/sitemap.xml", 6, "214 URLs") },
			{
				step: {
					tool: "write_file",
					intent: "Writing the first page",
					args: { file_path: "/home/jesse/sites/docs/index.html" },
					seconds: 0,
					running: true,
				},
			},
		],
		escalation: {
			tool: "write_file",
			kind: "file_tool",
			mode: "workspace-write",
			deniedPath: "/home/jesse/sites/docs/index.html",
		},
	},
	"s-tasklist": {
		model: "lunaroute/glm-5.3-vision",
		entries: [
			{
				user: "The inline task card in the transcript is too tall. Make it one line that expands, and keep the current task visible.",
			},
			{
				agent:
					"I'll fold `TaskCard` into one summary line (*→ Fold the task card · 1 of 4*) that expands in place, and keep the full list in the Tasks panel. Subagents will update the tests and the browser guard.",
			},
			{
				step: {
					tool: "read_file",
					intent: "Read the task card",
					args: { file_path: "cmd/evener-hub/frontend/src/panes/session/transcript/TaskCard.tsx" },
					seconds: 3,
				},
			},
			{
				step: {
					tool: "edit_file",
					intent: "Folded the card to one line",
					args: {
						file_path: "cmd/evener-hub/frontend/src/panes/session/transcript/TaskCard.tsx",
						old_string: "  return <section className={styles.card}>{rows}</section>;",
						new_string:
							"  if (!open) return <button className={styles.line} onClick={expand}>\n    → {current.title} · {done} of {total}\n  </button>;",
					},
					seconds: 40,
				},
			},
			{ step: shell("Ran the frontend tests", "npm test -- TaskCard", 25, "✓ 14 tests passed") },
			{ subagent: "t-1" },
			{ subagent: "t-2" },
			{ step: { ...shell("Running the hub tests", "go test ./cmd/evener-hub/...", 0), running: true } },
		],
		queued: ["Keep the expand arrow on the left, like the Tasks panel."],
		tasks: { total: 4, done: 1, remaining: 3, current: { id: 2, description: "Fold the task card to one line" } },
	},
	"s-stumble": {
		queued: [
			"Start with the edit_file stumbles; they're the most common.",
			"Keep the repair logging so we can count what it fixes.",
			"Don't change the tool schemas in this pass.",
			"Add a test for each stumble you repair.",
			"When you're done, rerun last week's corpus and report the rate.",
		],
		tasks: { total: 5, done: 2, remaining: 3, current: { id: 3, description: "Repair malformed tool arguments" } },
	},
	"s-retry": {
		model: "codex-jesse-fsck.com/gpt-5.6",
		effort: "high",
		entries: [
			{ user: "Sessions on codex get stuck retrying forever when the provider returns 429. Find the loop and cap it." },
			{
				agent:
					"I'll find where provider retries are scheduled, write a test that reproduces the endless loop, then cap retries with backoff.",
			},
			{ subagent: "r-1" },
			{ subagent: "r-2" },
			{
				warning: {
					text: "inspect delegate attention: open delegates.jsonl: permission denied",
					title: "Evener error",
					hint: "Check that the session's state directory is writable.",
				},
			},
			{
				agent:
					"The loop is in `llm/retry.go`: a 429 resets the attempt counter instead of incrementing it, so it never gives up. The test reproduces it. Now capping retries at 5 with exponential backoff.",
			},
			{
				step: {
					tool: "edit_file",
					intent: "Edited the retry loop",
					args: {
						file_path: "llm/retry.go",
						old_string: "for attempt := 0; ; attempt++ {\n\tif resp.StatusCode == 429 { attempt = 0 }",
						new_string:
							'for attempt := 0; ; attempt++ {\n\tif attempt >= maxRetries {\n\t\treturn nil, fmt.Errorf("gave up after %d attempts: %w", attempt, lastErr)\n\t}',
					},
					seconds: 30,
				},
			},
			{
				step: {
					...shell(
						"Ran the retry tests",
						"go test ./llm/... -run Retry",
						12,
						"llm: provider codex-jesse-fsck.com: 401 Unauthorized: sign-in expired\nFAIL\tgithub.com/prime-radiant-inc/evener/llm\t0.9s",
					),
					failed: { error: "exit status 1", exitCode: 1 },
				},
			},
			{ subagent: "r-3" },
		],
		error: {
			title: "codex-jesse-fsck.com sign-in expired (401)",
			message: "codex-jesse-fsck.com: 401 Unauthorized: sign-in expired",
			hint: "The session stopped after the provider refused 5 requests in a row. Sign in again, then retry.",
			cause: { kind: "provider", provider: "codex-jesse-fsck.com", model: "gpt-5.6", status: 401 },
		},
		tasks: { total: 4, done: 2, remaining: 2, current: { id: 3, description: "Cap retries per provider" } },
	},
	"s-jobdisp": {
		model: "lunaroute/glm-5.3-vision",
		entries: [
			{
				user: "Failed jobs in the transcript just say 'failed'. Show the exit code and the last lines of output inline.",
			},
			{
				step: {
					tool: "edit_file",
					intent: "Edited the job row",
					args: {
						file_path: "cmd/evener-hub/frontend/src/panes/session/transcript/JobRow.tsx",
						old_string: "  return <span className={styles.status}>failed</span>;",
						new_string:
							"  return (\n    <div className={styles.failed}>\n      <span className={styles.status}>exit {job.exitCode}</span>\n      <pre className={styles.tail}>{lastLines(job.output, 20)}</pre>\n    </div>\n  );",
					},
					seconds: 90,
				},
			},
			{ step: shell("Ran the tests", "npm test", 48, testOutput(60)) },
			{
				step: shell(
					"Opened PR #2331",
					"gh pr create --fill",
					6,
					"https://github.com/prime-radiant-inc/evener/pull/2331",
				),
			},
			{
				agent:
					"Done. Failed jobs now show the exit code and the last 20 lines of output inline, with a link to the full log. PR #2331 is open and green.",
			},
		],
		agentNote: "PR #2331 is green. I'll merge once you've looked at the new failed-job card.",
		links: [{ id: "u-2331", url: "https://github.com/prime-radiant-inc/evener/pull/2331", label: "PR #2331" }],
	},
	"s-roster": {
		humanNote: "Measure on magic-kingdom, not a laptop.",
		agentNote: "Roster p95 went from 1.8s to 240ms. The fix is in PR #2290.",
		links: [{ id: "u-2290", url: "https://github.com/prime-radiant-inc/evener/pull/2290", label: "PR #2290" }],
	},
};

// EVENER_DEMO_LONG: content longer than any Appendix A frame, so a
// screenshot pass exercises what real sessions carry: a long question with
// five long options among four, an approval with a deep path, and a working
// session with long messages from each side, many steps and daemon
// notifications (content from the 2026-09-29 device audit).
const LONG_AGENT = [
	"## Where the race lives",
	"",
	"The flaky tests all trace back to one ordering problem between the **tree settle pass** and the **retirement drain**. Both take `t.mu`, but settle releases it between phases, and the drain can observe a half-settled tree in that window. On a quiet machine the window is a few microseconds; under `-race` and on the CI runners it widens to milliseconds, which is why the failure rate jumps from 1 in 400 locally to 1 in 6 on CI.",
	"",
	"What I checked, in order:",
	"",
	"1. Every caller of `settle` in `agent/retirement.go`, `agent/subagents.go` and `agent/session_lifecycle.go` (eleven call sites).",
	"2. Whether the drain ever runs without the tree lock (it doesn't, except in `drainForTest`, which the failing tests don't use).",
	"3. The three failing tests' timelines, reconstructed from `-v` output with nanosecond timestamps.",
	"4. Whether the fix changes any public behavior (it doesn't: settle already promised to finish after drain; it just didn't enforce it).",
	"",
	"> The drain now waits for settle, and settle never waits on the drain. That keeps the lock order one-way and removes the window entirely.",
	"",
	"| Test | Local failures | CI failures | After fix |",
	"|---|---|---|---|",
	"| TestRetirementTreeSettleDrainsPendingRootAttention | 1/400 | 1/6 | 0/2000 |",
	"| TestFoldPublicationMarkersLandInOrder | 0/400 | 1/11 | 0/2000 |",
	"| TestQueueRetirementCancelsQueued | 2/400 | 1/4 | 0/2000 |",
	"",
	"```go",
	"func (t *tree) settle(ctx context.Context) error {",
	"\tif err := t.drain.wait(ctx); err != nil {",
	'\t\treturn fmt.Errorf("settle: wait for drain: %w", err)',
	"\t}",
	"\tt.mu.Lock()",
	"\tdefer t.mu.Unlock()",
	"\treturn t.settleLocked(ctx)",
	"}",
	"```",
	"",
	"I'll keep the subagents running the -race matrix until every package has 2,000 clean runs, then open the follow-up PR. If any package still flakes after that, I'll stop and show you the timeline before changing anything else.",
].join("\n");

const jobNotification = (id: string, desc: string, status: string, reason: string, excerpt: string) =>
	`<job-notification job_id="${id}" event="${status}" job_type="delegate" description="${desc}" status="${status}" reason="${reason}" intent="" output_bytes="${excerpt.length}" transcript_ref="magic-kingdom:${id}">\nJob ${id} ${status}. Output is available through read_transcript(transcript_ref="magic-kingdom:${id}") if needed.\nexcerpt:\n${excerpt}\n</job-notification>`;

const LONG_NOTICES: Entry[] = [
	{
		notice: jobNotification(
			"dlg_settle",
			"Fix race in tree settle",
			"failed",
			"go test exited 1 (3 times)",
			"--- FAIL: TestRetirementTreeSettleDrainsPendingRootAttention (0.44s)\n    retirement_test.go:212: settle finished before drain: got state idle, want draining\nFAIL\nexit status 1",
		),
	},
	{
		notice: jobNotification(
			"dlg_run0",
			"Run agent tests under -race on Linux",
			"completed",
			"",
			'{"message":"All 2,000 runs of the three tests passed under -race on magic-kingdom (linux/amd64). No new failures in agent/..., cmd/evener-hub/... or internal/....","concerns":[]}',
		),
	},
	{
		notice: `<delegate-notification delegate_id="dlg_drain" name="Check drain ordering in tests">{"kind":"reported","message":"Drain ordering is correct in every test except TestQueueRetirementCancelsQueued, which asserts on an intermediate state that the fix removes. I rewrote its assertion to check the final state instead.","warnings":["TestQueueRetirementCancelsQueued's assertion changed"]}</delegate-notification>`,
	},
	{
		notice: `${jobNotification("dlg_macos", "Run flaky tests 200 times on macOS", "completed", "", "200/200 passed")}\n${jobNotification("dlg_fold", "Verify fold publication markers", "failed", "context deadline exceeded", "panic: test timed out after 10m0s")}`,
	},
	{
		notice: `<job-notification job_id="dlg_watch" event="watch" job_type="delegate" description="Watch CI for PR 2138" status="running" reason="" intent="">\nJob dlg_watch running. 4 of 7 checks finished; lint and race-modules/agent still running.\n</job-notification>`,
	},
];

const LONG_STEPS: Entry[] = Array.from({ length: 24 }, (_, index) => ({
	step: shell(
		`Ran package ${index + 1} of 24 under -race`,
		`go test -race -count=200 ./agent/internal/pkg${index + 1}/...`,
		15 + index,
		`ok  \tgithub.com/prime-radiant-inc/evener/agent/internal/pkg${index + 1}\t${(index * 1.7 + 3).toFixed(1)}s`,
	),
}));

const LONG_USER =
	"Some more context, since last time this went sideways: the three tests that fail are all in the retirement path, and two of them only fail under -race. Please don't quarantine them, don't add retries, and don't raise any timeouts. If you find the fix touches the lock order anywhere else, stop and tell me before you change it. I'd also like a short write-up of the root cause in the plan so the next person who touches retirement understands why the drain waits on settle and never the other way round.";

// s-audit's ask, long: its first question grows long, its second stays, and
// two more follow.
function longAsk(questions: Question[]): Question[] {
	const [, second] = questions;
	return [
		{
			header: "Implied options",
			question:
				"Fourteen tool descriptions mention options their tools don't accept, and models keep trying them: should I drop the implied options from the descriptions, implement the missing flags so the descriptions become true, or split the difference tool by tool depending on how often each option is attempted in last week's transcripts?",
			why: "Fourteen descriptions mention flags the tools don't accept, like --force on read_file, timeout_ms on job_watch and priority on delegate. Models try them and fail, which costs a retry each time and sometimes derails a whole turn. In last week's transcripts I found 212 attempts at these options across 61 sessions; 140 of them were --force on read_file alone. Dropping the text is quick and safe but loses the hint for the three options that would be genuinely useful. Implementing the flags is about 400 lines plus tests and changes tool behavior, which means the web and the TUI need matching help text. A per-tool pass is slower but lets you decide each case on its merits.",
			multi_select: false,
			options: [
				{
					label: "Drop them",
					detail:
						"Remove the implied options from all 14 descriptions, and add a lint that fails when a description names an argument the schema doesn't declare",
					recommended: true,
				},
				{
					label: "Keep them and add the flags",
					detail:
						"Implement the 9 missing flags; about 400 lines, with tests, plus matching help text in the web and the TUI",
				},
				{
					label: "Implement only the three useful ones",
					detail:
						"Add --force to read_file, timeout_ms to job_watch and priority to delegate; drop the other eleven from the descriptions",
				},
				{
					label: "Ask me per tool",
					detail: "I'll list each of the 14 with its attempt count from last week's transcripts for a yes or no",
				},
				{
					label: "Leave the descriptions alone for now",
					detail: "Log each attempt instead, and revisit once we have a month of data",
				},
			],
		},
		...(second ? [second] : []),
		{
			header: "Report",
			question: "How should I report what I changed?",
			why: "The PR will touch 14 descriptions across 6 files.",
			multi_select: false,
			options: [
				{ label: "One PR with a table", detail: "Every tool, before and after, in the PR body", recommended: true },
				{ label: "One PR per tool group", detail: "Smaller reviews, more PRs" },
				{ label: "Just the diff", detail: "No write-up" },
			],
		},
		{
			header: "Tests",
			question: "Should the lint run in CI?",
			why: "It adds about 2 seconds to make lint.",
			multi_select: false,
			options: [
				{ label: "Yes, as part of make lint", detail: "Fails the build on a new mismatch", recommended: true },
				{ label: "No, run it by hand", detail: "A script under scripts/ with help text" },
			],
		},
	];
}

function withLongEntries(slug: string, change: (entries: Entry[]) => Entry[]): SessionContent {
	const content = CONTENT[slug];
	if (!content?.entries) throw new Error(`${slug} has no entries to lengthen`);
	return { ...content, entries: change(content.entries) };
}

const mirror = CONTENT["s-mirror"];
const LONG_CONTENT: Record<string, SessionContent> = {
	"s-pr2138": withLongEntries("s-pr2138", ([first, ...rest]) => [
		...(first ? [first] : []),
		{ user: LONG_USER },
		{ agent: LONG_AGENT },
		...LONG_STEPS,
		...LONG_NOTICES,
		...rest,
	]),
	"s-audit": withLongEntries("s-audit", (entries) =>
		entries.map((entry) => ("ask" in entry ? { ask: longAsk(entry.ask) } : entry)),
	),
	"s-mirror": {
		...mirror,
		...(mirror?.escalation
			? {
					escalation: {
						...mirror.escalation,
						deniedPath:
							"/home/jesse/sites/docs/reference/wire/v6/notifications/evener-navigation-invalidated-and-thread-resync-ordering-guarantees/index.html",
						partiallyRan: true,
					},
				}
			: {}),
	},
};

// data.js's `generic` transcript: the prompt, a first reply, a read, and the
// row's activity as the step still running while the session works.
function genericEntries(session: FleetSession): Entry[] {
	const entries: Entry[] = [
		{ user: `${session.title}.` },
		{ agent: "Starting on this now. I'll read the relevant code first and report back." },
		{ step: { tool: "read_file", intent: "Read the relevant files", args: { file_path: "README.md" }, seconds: 20 } },
	];
	if (session.state === "working") entries.push(liveStep(session.activity));
	if (session.state === "idle") entries.push({ agent: "Finished. Summary is above." });
	return entries;
}

// What a live thought has streamed so far: enough for the tray's estimate
// ("Thinking… · 17 tokens").
const DEMO_THOUGHT = "Weighing where the settle and drain passes each take the tree lock.";

// A working row's activity line as the step it describes.
function liveStep(activity = "Thinking"): Entry {
	if (activity === "Thinking") return { thinking: true };
	if (activity.startsWith("Running "))
		return { step: { ...shell(activity, activity.slice("Running ".length), 0), running: true } };
	const [verb, ...rest] = activity.split(" ");
	const tool = verb === "Editing" ? "edit_file" : verb === "Writing" ? "write_file" : "read_file";
	return { step: { tool, intent: activity, args: { file_path: rest.join(" ") }, seconds: 0, running: true } };
}

// The seconds each entry takes on the session's clock; a running step and a
// live thought take the rest of the turn.
function secondsOf(entry: Entry): number {
	if ("step" in entry) return entry.step.seconds;
	if ("user" in entry || "steer" in entry) return 10;
	if ("agent" in entry) return 30;
	return 5;
}

// The prototype's session states as a thread's status. A session that
// finished without asking ("yourmove") rests idle, as a daemon's does.
const THREAD_STATUS: Record<ProtoState, string> = {
	failed: "systemError",
	question: "awaiting",
	approval: "active",
	restart: "restartRequired",
	yourmove: "idle",
	working: "active",
	idle: "idle",
	shutdown: "notLoaded",
};

const SUBAGENT_STATUS = { running: "running", failed: "failed", done: "completed" } as const;
function flatten(subagents: RawSubagent[], parent?: string): { subagent: RawSubagent; parent?: string }[] {
	return subagents.flatMap((subagent) => [
		{ subagent, ...(parent ? { parent } : {}) },
		...flatten(subagent.children ?? [], subagent.id),
	]);
}

const iso = (ms: number) => new Date(ms).toISOString();
const callId = (subagentId: string) => `call-${subagentId}`;

// The session's whole subagent tree as the delegates its diagnostics carry,
// each tied to the transcript's delegate call by its tool call id. A daemon
// owns every delegate in a tree by its root (agent/delegate_tree_start.go);
// parentDelegateId marks the nesting.
function delegatesOf(session: FleetSession, now: number): EvenerDelegateInfo[] {
	return flatten(session.subagents).map(({ subagent, parent }) => {
		const running = subagent.state === "running";
		const lastActive = now - subagent.ago * 1000;
		return {
			runGeneration: 1,
			delegateId: subagent.id,
			ownerSessionId: demoSessionId(session.slug),
			rootSessionId: demoSessionId(session.slug),
			childSessionId: demoSessionId(subagent.id),
			transcriptRef: hostSessionRef(session.hostId, subagent.id),
			...(parent ? { parentDelegateId: parent } : {}),
			type: "delegate",
			lifecycle: "stable",
			// The hub closes a finished delegate that can't be resumed.
			phase: running ? "running" : "closed",
			status: SUBAGENT_STATUS[subagent.state],
			terminal: !running,
			// The activity tree carries the same outcome (demoSubagents.ts), so the
			// chip and the Subagents list classify this delegate alike (issue #2684).
			...(running ? {} : { outcome: subagent.state === "failed" ? "failed" : "completed" }),
			resumable: false,
			needsAttention: subagent.state === "failed",
			projectionRevision: 1,
			description: subagent.title,
			originToolCallId: callId(subagent.id),
			runStartedAt: iso(demoRunStartedAt(subagent, now)),
			...(running ? { latestActivityAt: iso(lastActive) } : { runEndedAt: iso(lastActive) }),
			...(subagent.state === "failed" ? { reason: FAILURE_REASONS[subagent.id] ?? GENERIC_FAILURE } : {}),
		};
	});
}

// The session's one turn, its entries laid end to end so the last one lands
// when the row says the session last changed.
function turnOf(session: FleetSession, entries: Entry[], error: TurnError | undefined, now: number): Turn {
	const end = now - session.ago * 1000;
	const start = end - entries.reduce((total, entry) => total + secondsOf(entry) * 1000, 0);
	const subagents = new Map(flatten(session.subagents).map(({ subagent }) => [subagent.id, subagent]));
	const id = `${session.slug}-turn`;
	let at = start;
	const items = entries.map((entry, index): ThreadItem => {
		const item = { id: `${id}-${index}`, startedAt: at };
		const seconds = secondsOf(entry);
		at += seconds * 1000;
		if ("user" in entry) return { ...item, type: "userMessage", text: entry.user, status: "completed" };
		if ("steer" in entry) return { ...item, type: "steering", source: "user", text: entry.steer, status: "completed" };
		if ("agent" in entry) return { ...item, type: "agentMessage", text: entry.agent, status: "completed" };
		// A live thought as the hub's overlay serves it: opened by a delta
		// with text, and with no startedAt, which the wire never carries for
		// reasoning.
		if ("thinking" in entry) return { id: item.id, type: "reasoning", text: DEMO_THOUGHT, status: "inProgress" };
		if ("ask" in entry) return askItem(item.id, `${id}-ask`, entry.ask, item.startedAt, at);
		if ("notice" in entry)
			return { ...item, type: "steering", text: entry.notice, steeringKind: "notification", status: "completed" };
		if ("warning" in entry) {
			const { text, title, hint } = entry.warning;
			return {
				...item,
				type: "systemMessage",
				eventKind: "warning",
				text,
				raw: { warning: { source: "evener", title, hint } },
				status: "completed",
			};
		}
		if ("recorded" in entry) {
			const recorded: ThreadItem = { ...entry.recorded, startedAt: item.startedAt, turnId: id };
			return recorded.status === "inProgress" || !("completedAt" in recorded)
				? recorded
				: { ...recorded, completedAt: at };
		}
		if ("subagent" in entry) {
			const subagent = subagents.get(entry.subagent);
			if (!subagent) throw new Error(`${session.slug} has no subagent ${entry.subagent}`);
			// The call settles as soon as its launch receipt returns, whatever
			// the subagent goes on to do, as agent's stableDelegateCreateTool
			// answers it; the row reads the subagent's state from its delegate.
			return {
				...item,
				type: "commandExecution",
				toolName: "delegate",
				callId: callId(subagent.id),
				description: subagent.title,
				argumentsJson: JSON.stringify({ description: subagent.title }),
				status: "completed",
				completedAt: at,
				output: JSON.stringify({
					delegate_id: subagent.id,
					child_session_id: demoSessionId(subagent.id),
					type: "delegate",
					status: "running",
					name: subagent.title,
					transcript_ref: hostSessionRef(session.hostId, subagent.id),
				}),
			};
		}
		const { step } = entry;
		return {
			...item,
			type: "commandExecution",
			toolName: step.tool,
			callId: `${id}-${index}-call`,
			description: step.intent,
			argumentsJson: JSON.stringify(step.args),
			status: step.running ? "inProgress" : step.failed ? "failed" : "completed",
			...(step.running ? {} : { completedAt: at }),
			...(step.output === undefined ? {} : { output: step.output }),
			...(step.failed ? { error: step.failed.error, exitCode: step.failed.exitCode } : {}),
		};
	});
	const active = THREAD_STATUS[session.state] === "active";
	return {
		id,
		itemsView: "full",
		status: active ? "inProgress" : error ? "failed" : "completed",
		items,
		startedAt: start,
		...(active ? {} : { completedAt: end, durationMs: end - start }),
		...(error ? { error } : {}),
	};
}

// A session's queued messages. Each one's entry id doubles as the id of the
// mutation that queued it, which the daemon lists beside the entries.
function queueOf(slug: string, texts: string[] = []): QueueState {
	if (texts.length === 0) return { revision: 0 };
	const ids = texts.map((_, index) => `${slug}-queued-${index}`);
	return {
		revision: 0,
		depth: texts.length,
		ids,
		clientMutationIds: [...ids],
		texts: [...texts],
		preview: queuePreview(texts),
	};
}

// A queue's preview: each entry's first 80 characters.
export function queuePreview(texts: readonly string[]): string[] {
	return texts.map((text) => text.slice(0, 80));
}

const NO_CAPABILITIES: ThreadCapabilities = {
	send: false,
	steer: false,
	interrupt: false,
	compact: false,
	clear: false,
	forkFromTurn: false,
	shutdown: false,
	changeModel: false,
	changeVisionModel: false,
	queue: false,
	goal: false,
	sharedNotes: false,
	rename: false,
	skillInput: false,
};

// A session's capabilities as the hub advertises them for its status:
// - live, a daemon's set (server/appwire_runtime.go's appCapabilitiesLocked),
//   with Send only while it rests and Clear as clearAvailable says;
// - shut down, the hub's past-session set (cmd/evener-hub/app_threadread.go's
//   pastThreadCapabilities), since sending resumes it;
// - needing a restart, readable notes alone
//   (cmd/evener-hub/internal/appsource/local_daemon.go);
// - a subagent while it runs, nothing: the hub serves it as a read-only alias
//   of its coordinator's process (cmd/evener-hub/app_rpc.go), so its screen
//   shows the bar in the composer's place (ruling 30). Once its run ends it
//   reads as a past session.
// Called whenever a session changes, so what it offers follows its state.
export function refreshCapabilities(thread: Thread): void {
	const status = thread.status.type;
	if (status === "restartRequired") {
		thread.evener.capabilities = { ...NO_CAPABILITIES, sharedNotes: true };
		return;
	}
	// What a live daemon and a past session both offer.
	const resumable: ThreadCapabilities = {
		...NO_CAPABILITIES,
		compact: true,
		shutdown: true,
		changeModel: true,
		changeVisionModel: true,
		queue: true,
		goal: true,
		sharedNotes: true,
		rename: true,
		skillInput: true,
	};
	const subagent = thread.evener.parentRef !== undefined;
	if (subagent && status === "active") {
		thread.evener.capabilities = NO_CAPABILITIES;
		return;
	}
	thread.evener.capabilities =
		subagent || SHUT_DOWN_STATUSES.has(status)
			? { ...resumable, send: true, clear: true, forkFromTurn: true }
			: {
					...resumable,
					send: status !== "active",
					steer: true,
					interrupt: true,
					clear: clearAvailable(thread),
				};
}

// Whether a live session can Clear: only at rest, with nothing queued or
// pending, and no question or approval waiting on you, as the daemon's
// clearBlockedReasonLocked says.
function clearAvailable(thread: Thread): boolean {
	return (
		thread.status.type !== "active" &&
		(thread.evener.queue.depth ?? 0) === 0 &&
		(thread.evener.pendingMutations ?? []).length === 0 &&
		!thread.evener.askPending &&
		(thread.evener.pendingEscalations ?? []).length === 0
	);
}

// A fleet session starts `turn`: it works, and its working line counts from
// `now`. It keeps Stop and steering as a daemon does, whatever the turn, so a
// queue held by Stop can still be sent (refreshCapabilities).
export function startFleetTurn(thread: Thread, turn: Turn, now: number): void {
	thread.status = { type: "active" };
	thread.evener.activeTurnId = turn.id;
	thread.evener.activeTurnStartedAt = now;
	thread.updatedAt = Math.floor(now / 1000);
	refreshCapabilities(thread);
}

// A fleet session stops running a turn and rests, idle or waiting on you.
export function restFleetSession(thread: Thread, status: "idle" | "awaiting", now: number): void {
	thread.status = { type: status };
	delete thread.evener.activeTurnId;
	delete thread.evener.activeTurnStartedAt;
	thread.updatedAt = Math.floor(now / 1000);
	refreshCapabilities(thread);
}

// A turn ends: its open items settle as a daemon records them (a tool call
// cut off by Stop reads "interrupted"), and the session's clocks take the
// turn's time.
export function endTurn(thread: Thread, turn: Turn, status: "completed" | "interrupted", now: number): void {
	for (const item of turn.items ?? [])
		if (item.status === "inProgress") {
			item.status = item.type === "commandExecution" ? status : "completed";
			item.completedAt = now;
		}
	turn.status = status;
	turn.completedAt = now;
	if (turn.startedAt !== undefined) {
		turn.durationMs = now - turn.startedAt;
		thread.evener.workMillis = (thread.evener.workMillis ?? 0) + turn.durationMs;
	}
	thread.evener.lastTurnEndedAt = now;
}

// The first question of the session's pending ask, as the daemon summarizes
// it for the Board's row (appwire/types.go's PendingQuestion).
function pendingQuestionOf(ask: Question[]): PendingQuestion {
	const [first] = ask;
	if (!first) throw new Error("a pending question needs an ask");
	return { question: first.question, options: first.options.map((option) => option.label), count: ask.length };
}

// An ask_user call, acknowledged: the question the phone's dock shows while
// the session waits on you.
function askItem(
	id: string,
	callId: string,
	questions: Question[],
	startedAt: number,
	completedAt: number,
): ThreadItem {
	return {
		id,
		type: "commandExecution",
		toolName: "ask_user",
		callId,
		argumentsJson: JSON.stringify({ questions }),
		status: "completed",
		startedAt,
		completedAt,
	};
}

// `parentRef` names the session a subagent's thread belongs to.
// Every tool family the phone summarizes, replayed from the recorded wire
// corpora rather than written by hand, to see and screenshot each one. Built
// only when served, so the usual sessions never read agent/testdata.
function toolFamiliesContent(): SessionContent {
	return {
		// The recorded shell calls cd here first, as a session's own directory.
		cwd: recordedToolCwd(),
		entries: [
			{ user: "Show me one step of every tool family." },
			...recordedToolFamilies().map((recorded): Entry => ({ recorded })),
			{ agent: "That's one of each: the core tools, subagents, the task list, notifications and system events." },
		],
	};
}

function sessionThread(session: FleetSession, now: number, parentRef?: string, long = false): Thread {
	const content =
		(long ? LONG_CONTENT[session.slug] : undefined) ??
		(session.slug === "s-tools" ? toolFamiliesContent() : CONTENT[session.slug]) ??
		{};
	const usage = content.usage ?? BASE_USAGE;
	const status = THREAD_STATUS[session.state];
	const active = status === "active";
	const entries = content.entries ?? genericEntries(session);
	const turn = turnOf(session, entries, content.error, now);
	// A hub names a thread by its session id (cmd/evener-hub/app_threadread.go),
	// the id part of the session's ref.
	const sessionId = demoSessionId(session.slug);
	const threadId = sessionId;
	const updatedAt = Math.floor((now - session.ago * 1000) / 1000);
	const thread: Thread = {
		id: threadId,
		sessionId,
		name: session.title,
		preview: session.title,
		ephemeral: false,
		modelProvider: content.model ?? DEFAULT_MODEL,
		createdAt: Math.floor((turn.startedAt ?? now) / 1000),
		updatedAt,
		status: { type: status },
		cwd: content.cwd ?? session.workingDir,
		projectPath: session.workingDir,
		gitInfo: { branch: "main" },
		cliVersion: "demo",
		source: "demo",
		turns: [turn],
		evener: {
			ref: session.ref,
			...(parentRef ? { parentRef } : {}),
			instanceId: `demo-instance-${session.slug}`,
			queue: queueOf(session.slug, content.queued),
			capabilities: NO_CAPABILITIES,
			...(active
				? {
						activeTurnId: turn.id,
						// A running subagent's turn is its run, timed as its row is.
						activeTurnStartedAt: session.runStartedAt ?? turn.startedAt,
					}
				: {}),
			...(turn.completedAt === undefined ? {} : { lastTurnEndedAt: turn.completedAt }),
			reasoningEffort: content.effort ?? DEFAULT_EFFORT,
			reasoningEffortLevels: ALL_EFFORTS,
			supportsReasoning: true,
			...(session.state === "question"
				? {
						askPending: true,
						pendingQuestion: pendingQuestionOf(entries.flatMap((entry) => ("ask" in entry ? entry.ask : []))),
					}
				: {}),
			...(status === "systemError" && content.error
				? {
						failure: {
							...(content.error.title ? { title: content.error.title } : {}),
							...(content.error.cause ? { cause: content.error.cause } : {}),
						},
					}
				: {}),
			...(content.escalation
				? {
						pendingEscalations: [
							{ threadId, ref: session.ref, escalationId: `${session.slug}-escalation`, ...content.escalation },
						],
					}
				: {}),
			...(content.tasks ? { tasks: content.tasks } : {}),
			...(content.goal ? { goal: content.goal } : {}),
			...(content.humanNote ? { humanNote: content.humanNote } : {}),
			...(content.agentNote ? { agentNote: content.agentNote } : {}),
			...(content.links ? { sessionUrls: content.links.map((link) => ({ ...link, addedBy: "agent" })) } : {}),
			diagnostics: {
				plugins: enabledPluginNames().map((name) => ({
					name,
					skillCount: 1,
					agentCount: 0,
					hookCount: 0,
					mcpCount: 0,
				})),
				delegates: delegatesOf(session, now),
			},
			usage: {
				inputTokens: usage.inputTokens,
				outputTokens: usage.outputTokens,
				cacheReadTokens: usage.cacheReadTokens,
				totalTokens: usage.inputTokens + usage.outputTokens + usage.cacheReadTokens,
			},
			cost: usage.cost,
			// data.js's `base` work time: the turns before this one. A running
			// turn's own time joins it when the turn ends, as the daemon counts.
			workMillis: 2 * 60 * 60 * 1000,
			contextUsed: usage.contextUsed,
			contextWindow: usage.contextWindow,
			access: { sandbox: "workspace-write", network: true },
		},
	};
	refreshCapabilities(thread);
	return thread;
}

export interface DemoSessionsOptions {
	// The instant the fleet's "ago" is measured from, as DemoFleetOptions.now.
	now?: number;
	// Mirrors EVENER_DEMO_LONG: LONG_CONTENT in place of the usual content.
	long?: boolean;
	// Mirrors EVENER_DEMO_FLEET_TOOLS: a thread for the tool families session.
	toolFamilies?: boolean;
}

// A thread for every fleet session, in the fleet's order, then one for every
// subagent in their trees, so every transcript ref a delegate names reads.
export function createDemoSessions(options: DemoSessionsOptions = {}): Thread[] {
	const now = options.now ?? Date.now();
	const sessions = fleetSessions(options.toolFamilies);
	return [
		...sessions.map((session) => sessionThread(session, now, undefined, options.long)),
		...sessions.flatMap((session) =>
			flatten(session.subagents).map(({ subagent, parent }) =>
				sessionThread(
					subagentSession(session, subagent, now),
					now,
					hostSessionRef(session.hostId, parent ?? session.slug),
				),
			),
		),
	];
}

// A subagent as a session of its own on its parent's host and folder, in the
// state its Board row shows, served with the generic transcript.
function subagentSession(owner: FleetSession, subagent: RawSubagent, now: number): FleetSession {
	return {
		slug: subagent.id,
		ref: hostSessionRef(owner.hostId, subagent.id),
		hostId: owner.hostId,
		title: subagent.title,
		state: SUBAGENT_SESSION_STATE[subagent.state],
		workingDir: owner.workingDir,
		ago: subagent.ago,
		subagents: subagent.children ?? [],
		runStartedAt: demoRunStartedAt(subagent, now),
	};
}

const SUBAGENT_SESSION_STATE = { running: "working", failed: "failed", done: "shutdown" } as const;

// The pid the demo's refusal names for a session's out-of-date daemon.
const DEMO_OUT_OF_DATE_DAEMON_PID = 48213;

// A session that needs a restart refuses a write, as the hub's
// daemonRestartRequiredError does (cmd/evener-hub/app_restart_required.go):
// its resume never reaches an out-of-date daemon, and the refusal blocks a
// retry of the same mutation.
function restartRequiredError(clientMutationId: string): WireError {
	return new WireError(
		`Session restart required: daemon pid ${DEMO_OUT_OF_DATE_DAEMON_PID} uses an incompatible protocol; this hub requires evener-appwire-v7. Stop the daemon, then resume this session. Stopping interrupts active work.`,
		-32013,
		{
			evenerErrorInfo: "conflict",
			cause: "daemonRestartRequired",
			clientMutationId,
			mutationOutcome: "unknown",
			retryDisposition: "blocked",
		},
	);
}

// A notes change must reach a session that takes shared notes, and name the
// instance it read. A stale instance is refused as the daemon refuses it
// (server/appwire_runtime.go, appwire.MutationNotAccepted). A shut-down
// session is resumed to take it, as the hub's setNotesHumanWithResume and
// removeURLWithResume do (cmd/evener-hub/app_session_resume.go).
function requireSharedNotes(thread: Thread, expectedInstanceId: string, clientMutationId: string, now: number): void {
	if (!thread.evener.capabilities.sharedNotes) throw new Error("This session doesn't take shared notes");
	if (expectedInstanceId !== thread.evener.instanceId)
		throw new WireError("thread instance is stale", -32013, {
			evenerErrorInfo: "conflict",
			clientMutationId,
			mutationOutcome: "notAccepted",
			retryDisposition: "none",
		});
	if (thread.status.type === "restartRequired") throw restartRequiredError(clientMutationId);
	if (SHUT_DOWN_STATUSES.has(thread.status.type)) restFleetSession(thread, "idle", now);
}

// The daemon's cap on a stored note, in runes (agent/session_notes.go).
const NOTE_MAX_RUNES = 1000;

// Go's unicode.IsSpace set, which the daemon's strings.Fields splits on; JS's \s
// differs (it adds U+FEFF and omits U+0085). Line feeds are split on first.
const NOTE_LINE_WHITESPACE = /[\t\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/u;
// A control character Go's unicode.IsSpace does not also call whitespace.
const NOTE_CONTROL = /[^\P{Cc}\t\n\v\f\r\u0085]/gu;

// A note as the daemon stores it (agent/session_notes.go's normalizeWhiteboard):
// control characters other than whitespace dropped, CRLF and CR turned into LF,
// each line's whitespace runs collapsed to one space and the line trimmed,
// blank lines at the start and end dropped and a run of blank lines kept as
// one, and the whole cut to NOTE_MAX_RUNES without a trailing space or line
// break.
function normalizeWhiteboard(text: string): string {
	const lines: string[] = [];
	let blankPending = false;
	for (const raw of text.replace(NOTE_CONTROL, "").replace(/\r\n?/g, "\n").split("\n")) {
		const line = raw.split(NOTE_LINE_WHITESPACE).filter(Boolean).join(" ");
		if (line === "") {
			// Only a blank line between two text lines survives.
			blankPending = lines.length > 0;
			continue;
		}
		if (blankPending) {
			lines.push("");
			blankPending = false;
		}
		lines.push(line);
	}
	return [...lines.join("\n")]
		.slice(0, NOTE_MAX_RUNES)
		.join("")
		.replace(/[ \n]+$/, "");
}

// The steer text for a changed note, as the daemon renders it
// (agent/session_notes_rpc.go's formatNotesField): continuation lines indented
// two spaces under the prefix, blank lines left empty. noteFromSteer reads it
// back.
export function humanNoteSteerText(note: string): string {
	if (note === "") return `${NOTE_STEER_PREFIX}${NOTE_STEER_CLEARED}`;
	return `${NOTE_STEER_PREFIX}${note.replace(/\n(?=[^\n])/g, "\n  ")}`;
}

// notes/human/set: your note replaces the session's. A changed note steers
// the agent in a turn of its own, which the receipt names; an unchanged one
// projects "removed" and wakes no one, as the daemon's does
// (agent/session_notes_rpc.go).
export function setHumanNote(thread: Thread, params: NotesHumanSetParams, now: number): NotesHumanSetResponse {
	requireSharedNotes(thread, params.expectedInstanceId, params.clientMutationId, now);
	const note = normalizeWhiteboard(params.note ?? "");
	const changed = note !== (thread.evener.humanNote ?? "");
	thread.evener.humanNote = note;
	let turnId: string | undefined;
	if (changed) {
		// The note steers the agent, recorded as the daemon records it
		// (agent/session_notes_rpc.go); the phone shows it as your note's row.
		const steer: ThreadItem = {
			id: `demo-note-${params.clientMutationId}`,
			type: "steering",
			source: "user",
			steeringKind: "human-note",
			text: humanNoteSteerText(note),
			clientMutationId: params.clientMutationId,
			status: "completed",
		};
		const running =
			thread.status.type === "active"
				? thread.turns?.find((turn) => turn.id === thread.evener.activeTurnId)
				: undefined;
		if (running) running.items?.push(steer);
		else {
			// A resting session wakes to read it, as the daemon's
			// wakeForPendingSteering starts a turn, which Stop can end.
			const turn: Turn = {
				id: `${thread.id}-note-${params.clientMutationId}`,
				itemsView: "full",
				status: "inProgress",
				startedAt: now,
				items: [{ ...steer, startedAt: now }],
			};
			thread.turns?.push(turn);
			startFleetTurn(thread, turn, now);
		}
		turnId = running?.id ?? thread.evener.activeTurnId;
	}
	return {
		note,
		receipt: {
			clientMutationId: params.clientMutationId,
			disposition: "applied",
			threadId: thread.id,
			...(thread.evener.instanceId ? { instanceId: thread.evener.instanceId } : {}),
			projectionState: changed ? "pending" : "removed",
			...(turnId ? { turnId } : {}),
		},
	};
}

// urls/remove: a link that isn't there is refused with the daemon's message,
// which the phone reads as already removed (sessionNotes.ts's removeLink).
export function removeLink(thread: Thread, params: UrlsRemoveParams, now: number): UrlsRemoveResponse {
	requireSharedNotes(thread, params.expectedInstanceId, params.clientMutationId, now);
	const links = thread.evener.sessionUrls ?? [];
	if (!links.some((link) => link.id === params.id))
		throw new Error(`no URL entry with id ${JSON.stringify(params.id)}`);
	thread.evener.sessionUrls = links.filter((link) => link.id !== params.id);
	return {};
}

// evener/sandbox/escalation/resolve: the approval leaves the session, which
// works on either way (the script shows neither the write nor the refusal).
export function resolveEscalation(thread: Thread, params: SandboxEscalationResolveParams): EmptyResponse {
	const pending = thread.evener.pendingEscalations ?? [];
	if (!pending.some((escalation) => escalation.escalationId === params.escalationId))
		throw new Error(`No pending escalation ${params.escalationId}`);
	thread.evener.pendingEscalations = pending.filter((escalation) => escalation.escalationId !== params.escalationId);
	return {};
}

// data.js's q-gateway: the question the working session asks when the demo
// fleet's EVENER_DEMO_FLEET_ASK_AFTER timer fires (demoFleet.ts's
// ASKING_SESSION_ID).
const WORKING_SESSION_QUESTION: Question[] = [
	{
		header: "Token storage",
		question: "Where should the gateway token command store tokens?",
		why: "Both work on Linux and macOS; the keychain is safer but needs a helper on headless hosts.",
		multi_select: false,
		options: [
			{ label: "System keychain", detail: "Safer; falls back to a file on headless hosts", recommended: true },
			{ label: "A file under ~/.config/evener", detail: "Simplest; mode 0600" },
			{ label: "Both, configurable", detail: "More code and more tests" },
		],
	},
];

// A staged event (demoFleet.ts's steps) as the session's thread shows it, so
// a banner opens a session that agrees with its Board row: a failure ends the
// running turn in an error, an approval waits on a sandbox escalation inside
// it, and a finish ends it with the ball in your court (a session that
// finished without asking rests idle, as THREAD_STATUS says).
export function stageFleetState(thread: Thread, state: "failed" | "approval" | "yourmove", now: number): void {
	if (state === "approval") {
		thread.evener.pendingEscalations = [
			{
				threadId: thread.id,
				ref: thread.evener.ref,
				escalationId: `${thread.evener.ref}-staged-escalation`,
				tool: "write_file",
				kind: "file_tool",
				mode: "workspace-write",
				deniedPath: "/home/jesse/sites/index.html",
			},
		];
		refreshCapabilities(thread);
		return;
	}
	const turn = thread.turns?.find((candidate) => candidate.id === thread.evener.activeTurnId);
	if (turn) endTurn(thread, turn, "completed", now);
	restFleetSession(thread, "idle", now);
	if (state === "failed") {
		thread.status = { type: "systemError" };
		thread.evener.failure = { title: "The turn stopped on an error" };
		refreshCapabilities(thread);
	}
}

// The working session asks its question: its turn ends on the ask, as a
// daemon's does, and the session waits on you, matching its Board row.
export function askWorkingSessionQuestion(thread: Thread, now: number): void {
	const turn = thread.turns?.find((candidate) => candidate.id === thread.evener.activeTurnId);
	if (!turn) throw new Error(`${thread.name} has no running turn to ask from`);
	turn.items?.push(askItem(`${turn.id}-ask`, `${turn.id}-ask`, WORKING_SESSION_QUESTION, now, now));
	endTurn(thread, turn, "completed", now);
	thread.evener.askPending = true;
	thread.evener.pendingQuestion = pendingQuestionOf(WORKING_SESSION_QUESTION);
	restFleetSession(thread, "awaiting", now);
}
