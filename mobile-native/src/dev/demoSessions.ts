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
// s-stumble queues five messages, more than the Session shows inline, so its
// "2 more queued" opens the Queue sheet.
import type {
	EmptyResponse,
	EvenerDelegateInfo,
	GoalState,
	ModelDescriptor,
	ModelListResponse,
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
import { canWriteHumanNote } from "../session/sessionNotes";
import {
	demoSessionId,
	enabledPluginNames,
	type FleetSession,
	fleetSessions,
	hostSessionRef,
	type ProtoState,
	type RawSubagent,
} from "./demoFleet";

const ALL_EFFORTS = ["low", "medium", "high", "xhigh", "max"];

// Two of data.js's provider profiles and their models, with its effort
// ladders and context windows. The recent model is one the fleet's sessions
// use that isn't the default.
const DEMO_MODELS: ModelDescriptor[] = [
	{
		provider: "lunaroute",
		model: "deepseek-4.1-flash",
		displayName: "DeepSeek 4.1 Flash",
		contextWindow: 256_000,
		supportsReasoning: true,
		reasoningEffortLevels: ALL_EFFORTS,
	},
	{
		provider: "lunaroute",
		model: "glm-5.3-vision",
		displayName: "GLM 5.3 Vision",
		contextWindow: 200_000,
		supportsVision: true,
		supportsReasoning: true,
		reasoningEffortLevels: ALL_EFFORTS,
	},
	{
		provider: "lunaroute",
		model: "glm-5.3-flash",
		displayName: "GLM 5.3 Flash",
		contextWindow: 128_000,
		supportsReasoning: true,
		reasoningEffortLevels: ["low", "medium", "high"],
	},
	{
		provider: "codex-jesse-fsck.com",
		model: "gpt-5.6",
		displayName: "GPT-5.6",
		contextWindow: 400_000,
		supportsVision: true,
		supportsReasoning: true,
		reasoningEffortLevels: ALL_EFFORTS,
	},
	{
		provider: "codex-jesse-fsck.com",
		model: "gpt-6-astra",
		displayName: "GPT-6 Astra",
		contextWindow: 1_000_000,
		supportsVision: true,
		supportsReasoning: true,
		reasoningEffortLevels: ALL_EFFORTS,
	},
];

export const DEMO_MODEL_LIST: ModelListResponse = {
	data: DEMO_MODELS,
	recent: DEMO_MODELS.filter((entry) => entry.model === "glm-5.3-vision"),
};

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
	| { ask: Question[] };

// What a frame's session carries beyond its fleet row.
interface SessionContent {
	entries?: Entry[];
	error?: TurnError;
	model?: string;
	effort?: string;
	queued?: string[];
	tasks?: TaskAggregate;
	goal?: GoalState;
	humanNote?: string;
	agentNote?: string;
	links?: Omit<SessionURL, "addedBy">[];
	escalation?: Pick<SandboxEscalationRequested, "tool" | "kind" | "mode" | "deniedPath">;
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
const SUBAGENT_ITEM_STATUS = { running: "inProgress", failed: "failed", done: "completed" } as const;

function flatten(subagents: RawSubagent[], parent?: string): { subagent: RawSubagent; parent?: string }[] {
	return subagents.flatMap((subagent) => [
		{ subagent, ...(parent ? { parent } : {}) },
		...flatten(subagent.children ?? [], subagent.id),
	]);
}

const iso = (ms: number) => new Date(ms).toISOString();
const callId = (subagentId: string) => `call-${subagentId}`;

// The session's whole subagent tree as the delegates its diagnostics carry,
// each tied to the transcript's delegate call by its tool call id.
function delegatesOf(session: FleetSession, now: number): EvenerDelegateInfo[] {
	return flatten(session.subagents).map(({ subagent, parent }) => {
		const running = subagent.state === "running";
		const lastActive = now - subagent.ago * 1000;
		return {
			delegateId: subagent.id,
			ownerSessionId: demoSessionId(parent ?? session.slug),
			rootSessionId: demoSessionId(session.slug),
			childSessionId: demoSessionId(subagent.id),
			transcriptRef: hostSessionRef(session.hostId, subagent.id),
			...(parent ? { parentDelegateId: parent } : {}),
			type: "delegate",
			lifecycle: "stable",
			phase: running ? "running" : "idle",
			status: SUBAGENT_STATUS[subagent.state],
			terminal: !running,
			resumable: false,
			needsAttention: subagent.state === "failed",
			projectionRevision: 1,
			description: subagent.title,
			originToolCallId: callId(subagent.id),
			runStartedAt: iso(lastActive - 5 * 60_000),
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
		// A thought with no text yet: the encoder leaves the empty text out.
		if ("thinking" in entry) return { ...item, type: "reasoning", status: "inProgress" };
		if ("ask" in entry) return askItem(item.id, `${id}-ask`, entry.ask, item.startedAt, at);
		if ("subagent" in entry) {
			const subagent = subagents.get(entry.subagent);
			if (!subagent) throw new Error(`${session.slug} has no subagent ${entry.subagent}`);
			return {
				...item,
				type: "commandExecution",
				toolName: "delegate",
				callId: callId(subagent.id),
				description: subagent.title,
				argumentsJson: JSON.stringify({ description: subagent.title }),
				status: SUBAGENT_ITEM_STATUS[subagent.state],
				...(subagent.state === "running" ? {} : { completedAt: at }),
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
//   (cmd/evener-hub/internal/appsource/local_daemon.go).
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
	thread.evener.capabilities = SHUT_DOWN_STATUSES.has(status)
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
	thread.updatedAt += 1;
	refreshCapabilities(thread);
}

// A fleet session stops running a turn and rests, idle or waiting on you.
export function restFleetSession(thread: Thread, status: "idle" | "awaiting"): void {
	thread.status = { type: status };
	delete thread.evener.activeTurnId;
	delete thread.evener.activeTurnStartedAt;
	thread.updatedAt += 1;
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

function sessionThread(session: FleetSession, now: number): Thread {
	const content = CONTENT[session.slug] ?? {};
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
		cwd: session.workingDir,
		projectPath: session.workingDir,
		gitInfo: { branch: "main" },
		cliVersion: "demo",
		source: "demo",
		turns: [turn],
		evener: {
			ref: session.ref,
			instanceId: `demo-instance-${session.slug}`,
			queue: queueOf(session.slug, content.queued),
			capabilities: NO_CAPABILITIES,
			...(active ? { activeTurnId: turn.id, activeTurnStartedAt: turn.startedAt } : {}),
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
}

// A thread for every fleet session, in the fleet's order.
export function createDemoSessions(options: DemoSessionsOptions = {}): Thread[] {
	const now = options.now ?? Date.now();
	return fleetSessions().map((session) => sessionThread(session, now));
}

// A notes change must reach a session that can take notes now, by the rule
// the phone's Notes sheet follows (canWriteHumanNote), and name the instance
// it read. A stale instance is refused as the daemon refuses it
// (server/appwire_runtime.go, appwire.MutationNotAccepted).
function requireSharedNotes(thread: Thread, expectedInstanceId: string, clientMutationId: string): void {
	const writable = canWriteHumanNote({
		status: thread.status,
		resumeRequired: thread.evener.resumeRequired ?? false,
		capabilities: thread.evener.capabilities,
	});
	if (!writable) throw new Error("This session can't take notes now");
	if (expectedInstanceId !== thread.evener.instanceId)
		throw new WireError("thread instance is stale", -32013, {
			evenerErrorInfo: "conflict",
			clientMutationId,
			mutationOutcome: "notAccepted",
			retryDisposition: "none",
		});
}

// The steering text a changed note opens with (agent/session_notes_rpc.go's
// humanNoteSteerPrefix).
const HUMAN_NOTE_STEER_PREFIX = "human updated their whiteboard:";
// The daemon's cap on a stored note, in runes (agent/session_notes.go).
const NOTE_MAX_RUNES = 1000;

// A note as the daemon stores it (agent/session_notes.go's normalizeNote):
// control characters other than whitespace dropped, whitespace runs collapsed
// to one space, and the whole cut to NOTE_MAX_RUNES.
function normalizeNote(text: string): string {
	const collapsed = text
		.replace(/[^\P{Cc}\s]/gu, "")
		.split(/\s+/)
		.filter(Boolean)
		.join(" ");
	return [...collapsed].slice(0, NOTE_MAX_RUNES).join("");
}

// notes/human/set: your note replaces the session's. A changed note steers
// the agent in a turn of its own, which the receipt names; an unchanged one
// projects "removed" and wakes no one, as the daemon's does
// (agent/session_notes_rpc.go).
export function setHumanNote(thread: Thread, params: NotesHumanSetParams, now: number): NotesHumanSetResponse {
	requireSharedNotes(thread, params.expectedInstanceId, params.clientMutationId);
	const note = normalizeNote(params.note ?? "");
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
			text: `${HUMAN_NOTE_STEER_PREFIX} ${note || "(whiteboard cleared)"}`,
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
export function removeLink(thread: Thread, params: UrlsRemoveParams): UrlsRemoveResponse {
	requireSharedNotes(thread, params.expectedInstanceId, params.clientMutationId);
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

// The working session asks its question: its turn ends on the ask, as a
// daemon's does, and the session waits on you, matching its Board row.
export function askWorkingSessionQuestion(thread: Thread, now: number): void {
	const turn = thread.turns?.find((candidate) => candidate.id === thread.evener.activeTurnId);
	if (!turn) throw new Error(`${thread.name} has no running turn to ask from`);
	turn.items?.push(askItem(`${turn.id}-ask`, `${turn.id}-ask`, WORKING_SESSION_QUESTION, now, now));
	endTurn(thread, turn, "completed", now);
	thread.evener.askPending = true;
	thread.evener.pendingQuestion = pendingQuestionOf(WORKING_SESSION_QUESTION);
	restFleetSession(thread, "awaiting");
}
