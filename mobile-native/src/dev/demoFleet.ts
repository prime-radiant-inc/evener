// A realistic fleet for the demo hub's navigation resources, so the
// redesign's Board (docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md
// section 7) can be seen and screenshotted without a real hub. The content is
// ported from the prototype's canonical fixture,
// docs/design/mobile/redesign/prototype/data.js (Appendix B): about 17-20 live
// top-level sessions across two hosts, subagent trees up to 54 (one archived
// session at 467), pin categories, projects and archived sessions. That file
// is a browser script (it assigns to `window.EV_DATA`), so its content is
// transcribed here rather than imported; the mapping choices are recorded
// inline as each raw session is turned into a NavigationSessionSummary row.
import {
	NAVIGATION_CATALOG_LIMIT,
	NAVIGATION_SECTION_LIMIT,
	relativeAge,
} from "@evener/appwire-client/state/navigation";
import { capability, wireV2 } from "@evener/appwire-client/testing/navigation";
import type {
	AuthListResponse,
	NavigationCapability,
	NavigationInvalidatedPayload,
	NavigationJobSummary,
	NavigationProjectSummary,
	NavigationReadParams,
	NavigationReadResponse,
	NavigationSessionSummary,
	PluginListResponse,
	SearchParams,
	SearchResponse,
	Source,
} from "@evener/appwire-client";

// The generation id the fleet's navigationCapability advertises in demo-hub.mts's
// initialize handshake. Every wireV2 response must carry the exact same id:
// the shared navigation store rejects any other generation as a mismatch
// (appwire-client/typescript/state/navigation/revalidator.ts's `validate`,
// "generation mismatch"). Exported for the tests that assert it.
export const DEMO_FLEET_GENERATION = "demo-fleet";
// Every resource shares one revision, bumped when the fleet changes (the
// question askQuestion below poses), so an invalidation's target revision is
// one the next read actually reaches: the navigation store refuses a
// response below the revision it was told to expect.
const respond = (revision: number, params: NavigationReadParams, data: unknown): NavigationReadResponse =>
	wireV2(params, data, `"demo-fleet-${revision}"`, revision, DEMO_FLEET_GENERATION);

const M = 60;
const H = 3600;
const D = 86400;

// The hub's own truncation policy for a session's children
// (maxNavigationChildren in cmd/evener-hub/navigation_projection.go): past
// this many, the rest are counted in omitted_descendants instead of sent.
// Mirrored here so a swarm as busy as s-pr2138 (54) or s-fuzz (467) behaves
// like the real hub instead of shipping an unbounded payload.
const MAX_CHILDREN = 50;

// The hub's projectNode applies that same cap recursively, at every depth of
// a session's descendant tree, not only its immediate children -- so this is
// shared by both toRow (a session's own children) and toChildRow (a child's
// own children), rather than the top level capping and a nested level not.
export function capChildren<T>(children: T[]): { capped: T[]; omitted: number } {
	const capped = children.slice(0, MAX_CHILDREN);
	return { capped, omitted: children.length - capped.length };
}

const ARCHIVED_TOTAL = 271; // data.js: archivedTotal (Board mockup: "ARCHIVED · 271")
// How many archived rows a "project" overview embeds as a preview, versus a
// project_page(tier=archived) read paging through the real, full list.
const PROJECT_OVERVIEW_ARCHIVED_PREVIEW = 5;

// Names cycled through for a swarm whose members aren't individually named in
// the fixture, copied verbatim from data.js's swarmNames.
const SWARM_NAMES = [
	"Reproduce TestRetirementTreeSettle locally", "Bisect settle ordering change", "Check drain ordering in tests", "Audit delegate settle callers",
	"Run agent tests under -race", "Run hubcore tests under -race", "Check appwire projector ordering", "Stabilize TestFoldPublicationMarkers",
	"Trace retirement drain wakeups", "Verify job tree revisions", "Run cmd/evener-hub browser guards", "Check interrupt marker ownership",
	"Explore retirement drain callers", "Review settle lock scope", "Run linux -race on agent", "Check TestQueueRetirement flake",
	"Read CI logs for run 34717544502", "Compare failing seeds", "Check compaction fold timing", "Audit watch timer shutdown",
	"Run jobstore descendant merge tests", "Check delegate attention flags", "Stabilize TestTranscriptPaging", "Probe navigation invalidation order",
	"Run make lint on changed modules", "Check go vet windows tags", "Replay failing job trees", "Measure settle pass duration",
	"Check restart continuity path", "Verify cancel queued on retire", "Run hub relay tests", "Inspect secret-scan output",
	"Check stacked merge duplication", "Run appwire codec tests", "Verify ask pending clears", "Run TUI transcript suite",
	"Check mutation outbox replay", "Run native shared-session tests", "Stabilize TestRelayCloseFrame", "Check projector item paging",
	"Run fuzz smoke on tool args", "Verify steering injection order", "Check delegate budget accounting", "Run workspace isolation tests",
	"Verify lane branch disposal", "Check CI cache keys", "Run makefile audits", "Check TestHostAttachRetry",
	"Verify session namer fallback", "Run SDK package qualification", "Check provider retry caps", "Run docs freshness check",
];

type ProtoHost = "magic-kingdom" | "paradise-park";
// The prototype's own state vocabulary (data.js sessions[].state), mapped to
// the wire's below. "shutdown" covers every non-live session (shut down,
// test-run and archived alike -- data.js sets `live: false` on all of them).
type ProtoState = "failed" | "question" | "approval" | "restart" | "yourmove" | "working" | "idle" | "shutdown";
type SubState = "running" | "failed" | "done";

interface RawSubagent {
	id: string;
	title: string;
	state: SubState;
	ago: number;
	children?: RawSubagent[];
}

interface RawSession {
	id: string;
	title: string;
	host?: ProtoHost; // default magic-kingdom
	project?: string; // default "evener"
	state: ProtoState;
	ago: number; // seconds before startup, data.js's "ago"
	category?: "release" | "research"; // pin category, from data.js's `category`
	archived?: boolean;
	test?: boolean;
	activity?: string; // data.js's `activity`; a "Running <cmd>" one becomes a running job
	subs?: { run: number; fail: number; done: number }; // generic subagent counts (data.js genericSubs)
	children?: RawSubagent[]; // explicitly named subagents (data.js's `subagents` map)
}

// data.js's swarm for s-pr2138: two named failures plus 31 running, 3 waiting
// and 18 done generated ones (i in the same ranges, same modulo arithmetic).
const PR2138_CHILDREN: RawSubagent[] = [
	{
		id: "g-settle",
		title: "Fix race in tree settle",
		state: "failed",
		ago: 6 * M,
		children: [{ id: "g-settle-1", title: "Check drain ordering in tests", state: "running", ago: 20 }],
	},
	{ id: "g-repro", title: "Reproduce TestRetirementTreeSettleDrains", state: "failed", ago: 9 * M },
	...Array.from({ length: 31 }, (_, i) => ({
		id: `g-run-${i}`,
		title: SWARM_NAMES[(i + 3) % SWARM_NAMES.length] as string,
		state: "running" as const,
		ago: 5 + ((i * 7) % 90),
	})),
	...Array.from({ length: 3 }, (_, i) => ({
		id: `g-wait-${i}`,
		title: SWARM_NAMES[(i + 40) % SWARM_NAMES.length] as string,
		state: "done" as const,
		ago: (3 + i) * M,
	})),
	...Array.from({ length: 18 }, (_, i) => ({
		id: `g-done-${i}`,
		title: SWARM_NAMES[(i + 20) % SWARM_NAMES.length] as string,
		state: "done" as const,
		ago: (10 + i * 2) * M,
	})),
];

const RETRY_CHILDREN: RawSubagent[] = [
	{ id: "r-1", title: "Find where retries are scheduled", state: "done", ago: 2 * H },
	{ id: "r-2", title: "Write a test for the 429 loop", state: "done", ago: 90 * M },
	{ id: "r-3", title: "Cap retries with backoff", state: "failed", ago: 3 * M },
	{ id: "r-4", title: "Check other providers for the same loop", state: "done", ago: 4 * M },
];

const TASKLIST_CHILDREN: RawSubagent[] = [
	{ id: "t-1", title: "Update TaskCard tests", state: "running", ago: 4 },
	{ id: "t-2", title: "Update the browser guard", state: "running", ago: 9 },
	{ id: "t-3", title: "Check the Tasks panel", state: "running", ago: 7 },
	{ id: "t-4", title: "Measure card height", state: "done", ago: 6 * M },
];

const HIER_CHILDREN: RawSubagent[] = [
	{ id: "h-a", title: "Mock layout A: project, then host", state: "done", ago: 80 * M },
	{ id: "h-b", title: "Mock layout B: host badges in projects", state: "done", ago: 78 * M },
	{ id: "h-c", title: "Mock layout C: host, then project", state: "done", ago: 77 * M },
	{ id: "h-d", title: "Count sessions per project and host", state: "done", ago: 2 * H },
	{ id: "h-e", title: "Review the three mocks", state: "done", ago: 70 * M },
	{ id: "h-f", title: "Write the plan", state: "done", ago: 65 * M },
];

// Mirrors data.js's genericSubs: a session with only subagent *counts* gets
// members named by cycling SWARM_NAMES from a seed derived from its id, in
// the same order (failed, then running, then done) and with the same per-
// state `ago`.
function genericChildren(sessionId: string, counts: { run: number; fail: number; done: number }): RawSubagent[] {
	const prefix = `${sessionId}-g`;
	const pick = (i: number) => SWARM_NAMES[(prefix.length * 7 + i * 3) % SWARM_NAMES.length] as string;
	const out: RawSubagent[] = [];
	let k = 0;
	for (let i = 0; i < counts.fail; i++) out.push({ id: `${prefix}${k}`, title: pick(k++), state: "failed", ago: 20 * M });
	for (let i = 0; i < counts.run; i++) out.push({ id: `${prefix}${k}`, title: pick(k++), state: "running", ago: 10 });
	for (let i = 0; i < counts.done; i++) out.push({ id: `${prefix}${k}`, title: pick(k++), state: "done", ago: 40 * M });
	return out;
}

// The fixture's 34 top-level sessions, grouped exactly as data.js comments
// them. Live ones (everything above "Shut down, not live") total 20, matching
// the redesign spec's own Board mockup ("Live 20 (4)", section 7.1) -- more
// than Appendix B's rounded "about 17", but this file is the fixture Appendix
// B calls canonical, and 20 is what it actually contains.
const SESSIONS: RawSession[] = [
	// Needs you (4)
	{
		id: "s-retry",
		title: "Fix Endless Provider Retry Loop",
		host: "paradise-park",
		state: "failed",
		ago: 2 * M,
		children: RETRY_CHILDREN,
	},
	{ id: "s-audit", title: "Audit Tool Descriptions for Implied Options", state: "question", ago: 14 * M, subs: { run: 0, fail: 0, done: 8 } },
	{ id: "s-mirror", title: "Mirror Docs Site Locally", project: "prime-radiant-inc.github.io", state: "approval", ago: 21 * M },
	{ id: "s-namer", title: "Tune Session Namer Token Cap", state: "restart", ago: 47 * M },

	// Finished, not yet seen (4)
	{ id: "s-hier", title: "Host Project Hierarchy UI Mockups", state: "yourmove", ago: 62 * M, children: HIER_CHILDREN },
	{ id: "s-flakes", title: "Find Test Flakes in GitHub Issues", state: "yourmove", ago: 8 * M },
	{
		id: "s-jobdisp",
		title: "Redesign Failed Job Display",
		state: "yourmove",
		ago: 25 * M,
		category: "release",
		subs: { run: 0, fail: 1, done: 11 },
	},
	{ id: "s-branch", title: "Idiomatic Branch Naming for Delegates", state: "yourmove", ago: 3 * H },

	// Working (9)
	{
		id: "s-pr2138",
		title: "Get PR 2138 Test Clean",
		state: "working",
		ago: 5,
		category: "release",
		children: PR2138_CHILDREN,
		activity: "Waiting on 31 subagents",
	},
	{
		id: "s-tasklist",
		title: "Rework Inline Task List Display",
		state: "working",
		ago: 3,
		children: TASKLIST_CHILDREN,
		activity: "Running go test ./cmd/evener-hub/...",
	},
	{
		id: "s-stumble",
		title: "Diagnose and Fix Tool Use Stumbles",
		state: "working",
		ago: 8,
		subs: { run: 4, fail: 0, done: 5 },
		activity: "Editing agent/tool_repair.go",
	},
	{ id: "s-gateway", title: "Design Gateway Token Command MVP", state: "working", ago: 2, activity: "Thinking" },
	{
		id: "s-wasm",
		title: "Port Allocator to WASM Target",
		host: "paradise-park",
		project: "c-to-wasm",
		state: "working",
		ago: 12 * M,
		subs: { run: 2, fail: 0, done: 1 },
		activity: "Running make test-wasm",
	},
	{ id: "s-resume", title: "Fix Missing Prompt on Session Resume", state: "working", ago: 4, activity: "Reading agent/session_resume.go" },
	{
		id: "s-readintent",
		title: "Fix Missing File Read Intent Lines",
		state: "working",
		ago: 6,
		subs: { run: 1, fail: 0, done: 2 },
		activity: "Running make lint",
	},
	{
		id: "s-landing",
		title: "Draft Agent Directory Landing Copy",
		project: "alltheagents-org",
		state: "working",
		ago: 9,
		activity: "Writing site/index.md",
	},
	{
		id: "s-sdk",
		title: "Lift Ask Dock Into Shared Client",
		host: "paradise-park",
		state: "working",
		ago: 11,
		subs: { run: 2, fail: 0, done: 4 },
		// Spelled out in prose, not as a path: scripts/sdk/
		// package-import-paths-check.sh greps every quoted string in this tree
		// for the AppWire package spelled by path, and cannot tell this fixture
		// string from a real import (its own header comment says so).
		activity: "Running npm test in the AppWire TypeScript package",
	},

	// Idle, seen (3)
	{ id: "s-diff", title: "Evener Differentiation Rationale Doc", state: "idle", ago: 2 * H, category: "research" },
	{ id: "s-deslop", title: "Deslop README Pass", project: "deslop", state: "idle", ago: 5 * H },
	{ id: "s-shepherd", title: "Shepherd PR Stack Rebase", project: "shepherd-pr", state: "idle", ago: 7 * H },

	// Shut down, not live -- appear under Projects (6)
	{ id: "s-roster", title: "Hub Roster Latency", state: "shutdown", ago: 1 * D },
	{ id: "s-sandbox", title: "Sandbox Network Egress Audit", state: "shutdown", ago: 2 * D },
	{ id: "s-skills", title: "Skills Lifecycle Cleanup", project: "superpowers", state: "shutdown", ago: 3 * D },
	{ id: "s-house", title: "Thermostat Schedule Script", host: "paradise-park", project: "house", state: "shutdown", ago: 4 * D },
	{ id: "s-copy", title: "Tighten Pricing Page Copy", project: "copy-writing", state: "shutdown", ago: 5 * D },
	{ id: "s-wasm2", title: "WASM Linker Symbol Clash", host: "paradise-park", project: "c-to-wasm", state: "shutdown", ago: 2 * D },

	// Test runs (3)
	{ id: "s-test1", title: "Live Stack Smoke: Session on Host", project: "hub-test-env", state: "shutdown", ago: 20 * H, test: true },
	{ id: "s-test2", title: "Retirement Browser Guard Run", project: "hub-test-env", state: "shutdown", ago: 26 * H, test: true },
	{ id: "s-test3", title: "Spawn Guard Fixture Session", project: "hub-test-env", state: "shutdown", ago: 30 * H, test: true },

	// Archived (5)
	{ id: "s-gocache", title: "Investigate Go Test Caching", state: "shutdown", ago: 6 * D, archived: true },
	{
		id: "s-fuzz",
		title: "Harvest Fuzz Corpus Across Modules",
		state: "shutdown",
		ago: 8 * D,
		archived: true,
		subs: { run: 0, fail: 12, done: 455 },
	},
	{ id: "s-audit1", title: "Audit Tool Descriptions First Pass", state: "shutdown", ago: 9 * D, archived: true },
	{ id: "s-pairing", title: "Pairing Endpoint Review", state: "shutdown", ago: 12 * D, archived: true },
	{ id: "s-daemon", title: "Daemon Idle Retirement Timer", state: "shutdown", ago: 15 * D, archived: true },

	// data.js pins archivedTotal at 271 (Board mockup: "ARCHIVED · 271")
	// without individually naming 266 of them. These fill that count with
	// plain, clearly-generic entries so the archived tier is a real, fully
	// pageable list of 271 -- the hub's own archived tier is real paged rows,
	// never five real ones plus a promise of 266 more that never arrive --
	// instead of a page that stalls the moment a client asks for more than
	// the 5 named above.
	...Array.from({ length: ARCHIVED_TOTAL - 5 }, (_, i) => ({
		id: `s-archived-filler-${i}`,
		title: SWARM_NAMES[i % SWARM_NAMES.length] as string,
		state: "shutdown" as const,
		archived: true,
		ago: (16 + i) * D, // continues after the 5 named ones (6-15 days ago)
	})),
];

// The prototype's known projects (data.js's `projects`), plus the synthetic
// "hub-test-env" project data.js's comment says test-run sessions belong to.
// working_dir mirrors magic-kingdom's root ("/home/jesse/git") plus data.js's
// path for each; "home" is the bare root.
const PROJECT_META: { key: string; workingDir: string }[] = [
	{ key: "evener", workingDir: "/home/jesse/git/prime-radiant-inc/evener" },
	{ key: "c-to-wasm", workingDir: "/home/jesse/git/c-to-wasm" },
	{ key: "prime-radiant-inc.github.io", workingDir: "/home/jesse/git/prime-radiant/prime-radiant-inc.github.io" },
	{ key: "alltheagents-org", workingDir: "/home/jesse/git/prime-radiant/alltheagents-org" },
	{ key: "superpowers", workingDir: "/home/jesse/git/superpowers" },
	{ key: "deslop", workingDir: "/home/jesse/git/deslop" },
	{ key: "shepherd-pr", workingDir: "/home/jesse/git/shepherd-pr" },
	{ key: "house", workingDir: "/home/jesse/git/house" },
	{ key: "copy-writing", workingDir: "/home/jesse/git/copy-writing" },
	{ key: "home", workingDir: "/home/jesse" },
];

// data.js's `plugins`, minus fields the wire type doesn't carry (desc, counts,
// the optional newer-version hint).
const PLUGINS: { id: string; mp: string; on: boolean; version: string }[] = [
	{ id: "superpowers", mp: "superpowers-marketplace", on: true, version: "6.4.1" },
	{ id: "elements-of-style", mp: "superpowers-marketplace", on: true, version: "1.2.0" },
	{ id: "claude-session-driver", mp: "superpowers-marketplace", on: true, version: "0.9.3" },
	{ id: "private-journal-mcp", mp: "superpowers-marketplace", on: true, version: "2.1.0" },
	{ id: "superpowers-chrome", mp: "superpowers-marketplace", on: false, version: "1.4.2" },
	{ id: "frontend-design", mp: "claude-plugins-official", on: true, version: "1.0.0" },
	{ id: "go", mp: "go-skills", on: true, version: "3.2.0" },
	{ id: "go-release", mp: "go-skills", on: false, version: "1.1.0" },
	{ id: "go-spec-reviewer", mp: "go-skills", on: false, version: "0.4.0" },
	{ id: "fileflow-pathologize", mp: "go-skills", on: true, version: "0.2.1" },
	{ id: "iterative-development", mp: "prime-radiant-marketplace", on: true, version: "1.0.3" },
	{ id: "shepherd-pr", mp: "prime-radiant-marketplace", on: true, version: "0.7.0" },
	{ id: "study-skills", mp: "prime-radiant-marketplace", on: false, version: "0.3.0" },
	{ id: "simplify-code", mp: "simplify-code-dev", on: true, version: "0.5.0" },
];

// The one provider the Board's notice needs (data.js: codex-jesse-fsck.com,
// "Sign-in expired"); the other 10 providers in data.js aren't needed for the
// one thing requirement 2 asks the auth list to prove (the sign-in notice).
const EXPIRED_PROVIDER = "codex-jesse-fsck.com";

function hostId(host: ProtoHost | undefined): string {
	return (host ?? "magic-kingdom") === "magic-kingdom" ? "local" : "paradise-park";
}

// The prototype's states on the wire. An approval stays "active" (the phone
// infers approval from the row's presence in needs_you, plus approvalPending
// saying why), and "yourmove" is a turn that ended without asking.
const WIRE_STATE: Record<ProtoState, { state: string; askPending?: true; approvalPending?: true }> = {
	failed: { state: "errored" },
	question: { state: "awaiting", askPending: true },
	approval: { state: "active", approvalPending: true },
	restart: { state: "restartRequired" },
	yourmove: { state: "awaiting" },
	working: { state: "active" },
	idle: { state: "idle" },
	shutdown: { state: "ended" },
};

// The prototype's needs-you band, from core.js's NEEDS (this fixture has no
// "warning" state). It keys on the prototype's state because an approval row
// is "active" on the wire, like the working band.
const NEEDS_YOU_STATES = new Set<ProtoState>(["failed", "question", "approval", "restart"]);

const SUBAGENT_WIRE_STATE: Record<SubState, { state: string; live: boolean }> = {
	running: { state: "active", live: true },
	failed: { state: "errored", live: false },
	done: { state: "ended", live: false },
};

// cmd/evener-hub/navigation_projection.go's projectShallow sets Offline the
// same way for every projected node, subagents included -- it reads the
// row's own HostID, not anything about its parent -- so a session's
// children from an offline source are offline too, not just the parent row.
function toChildRow(
	sub: RawSubagent,
	ownerHostId: string,
	project: string,
	startupMs: number,
	offline: boolean,
): NavigationSessionSummary {
	const { state, live } = SUBAGENT_WIRE_STATE[sub.state];
	const { capped, omitted } = capChildren(sub.children ?? []);
	return {
		ref: `${ownerHostId}:${sub.id}`,
		host_id: ownerHostId,
		session_id: sub.id,
		title: sub.title,
		project,
		state,
		kind: "subagent",
		live,
		...(offline ? { offline: true as const } : {}),
		updated_at: new Date(startupMs - sub.ago * 1000).toISOString(),
		...(omitted > 0 ? { omitted_descendants: omitted } : {}),
		children: capped.map((child) => toChildRow(child, ownerHostId, project, startupMs, offline)),
	};
}

// Explicitly named subagents (RETRY_CHILDREN etc.) or ones generated from
// subs counts (genericChildren) -- never both; data.js does the same (a
// session either names its subagents or just counts them).
function rawChildren(raw: RawSession): RawSubagent[] {
	if (raw.children) return raw.children;
	if (raw.subs) return genericChildren(raw.id, raw.subs);
	return [];
}

// "Running <command>" activity lines become a running job; anything else
// ("Thinking", "Editing ...", "Waiting on N subagents") is not a command.
function runningJobs(raw: RawSession): NavigationJobSummary[] | undefined {
	if (!raw.activity?.startsWith("Running ")) return undefined;
	return [
		{
			job_id: `${raw.id}-job`,
			job_type: "bash",
			status: "running",
			command: raw.activity.slice("Running ".length),
		},
	];
}

function toRow(raw: RawSession, startupMs: number, offlineHost: boolean): NavigationSessionSummary {
	const owner = hostId(raw.host);
	const project = raw.project ?? "evener";
	const { state, askPending, approvalPending } = WIRE_STATE[raw.state];
	const live = raw.state !== "shutdown";
	const offline = owner === "paradise-park" && offlineHost;
	const { capped, omitted } = capChildren(rawChildren(raw));
	const jobs = runningJobs(raw);
	return {
		ref: `${owner}:${raw.id}`,
		host_id: owner,
		session_id: raw.id,
		title: raw.title,
		project,
		state,
		kind: "session",
		live,
		...(askPending ? { ask_pending: true as const } : {}),
		...(approvalPending ? { approval_pending: true as const } : {}),
		...(offline ? { offline: true as const } : {}),
		updated_at: new Date(startupMs - raw.ago * 1000).toISOString(),
		...(omitted > 0 ? { omitted_descendants: omitted } : {}),
		...(jobs ? { running_jobs: jobs } : {}),
		children: capped.map((child) => toChildRow(child, owner, project, startupMs, offline)),
	};
}

// A project's sessions appear under Projects only when they're neither
// archived nor a test run (those get their own catalogs), split "today"
// (current) from "recent" the way the web does (spec 7.1), using the same
// `ago` the row's age already carries.
const underProjects = (raw: RawSession) => !raw.archived && !raw.test;

function projectSessionsRaw(sessions: RawSession[], projectKey: string): RawSession[] {
	return sessions.filter((raw) => underProjects(raw) && (raw.project ?? "evener") === projectKey);
}

// The hosts that own a project's sessions, in the hub's own shape
// (NavigationProjectSummary.sources): "local" for this hub's sessions and a
// host's name for its own, omitted when this hub owns every one.
function projectSources(sessions: RawSession[], projectKey: string): string[] | undefined {
	const owners = new Set(sessions.filter((raw) => (raw.project ?? "evener") === projectKey).map((raw) => hostId(raw.host)));
	if ([...owners].every((owner) => owner === "local")) return undefined;
	return ["local", "paradise-park"].filter((owner) => owners.has(owner));
}

function projectSummary(sessions: RawSession[], key: string, sessionCount: number): NavigationProjectSummary {
	const meta = PROJECT_META.find((project) => project.key === key);
	if (!meta) throw new Error(`Unknown demonstration project: ${key}`);
	const sources = projectSources(sessions, key);
	return { key, name: key, working_dir: meta.workingDir, session_count: sessionCount, ...(sources ? { sources } : {}) };
}

export interface DemoFleetOptions {
	// The instant "ago" is measured from; every row's updated_at is fixed
	// relative to this at creation, matching a real fleet (ages grow as the
	// demo hub keeps running). Defaults to the moment the fleet is built.
	now?: number;
	// Mirrors EVENER_DEMO_FLEET_OFFLINE_HOST: marks paradise-park's source
	// offline and every one of its rows offline, for the offline frames.
	offlineHost?: boolean;
	// Mirrors EVENER_DEMO_FLEET_EMPTY: serves a hub with nothing live -- zero
	// sessions, so the manifest, sections, pin catalog, project catalogs and
	// search all come back empty. The sources still name a host (a real empty
	// hub is still a hub), so only offlineHost touches them. For the Board's
	// EmptyBoard state.
	empty?: boolean;
	// Mirrors EVENER_DEMO_FLEET_ASK_AFTER: that many seconds after the demo
	// hub starts, the working row s-gateway ("Design Gateway Token Command
	// MVP") asks a question, moving from Working into Needs you, so the
	// Board's spring and amber wash can be watched on a still list. The fleet
	// itself only knows how to make the change (askQuestion); demo-hub.mts
	// owns the timer and sends the resulting invalidation, so a test fires the
	// change by calling askQuestion directly instead of sleeping.
	askAfterSeconds?: number;
	// The clock evener/search's `age` reads, sampled fresh on every call --
	// unlike `now` above, which freezes each row's updated_at once at
	// startup. Defaults to Date.now; a test injects a fixed function so the
	// computed age doesn't depend on when the test happens to run.
	clock?: () => number;
}

interface FleetAnswers {
	answerNavigationRead(params: NavigationReadParams): NavigationReadResponse;
	answerSearch(params: SearchParams): SearchResponse;
	answerAuthList(): AuthListResponse;
	answerPluginList(): PluginListResponse;
}

export interface DemoFleet extends FleetAnswers {
	// demo-hub.mts's handshake capability, carrying the sequence the fleet
	// has reached: the navigation store refuses a reconnect whose sequence
	// moved backward within the generation. Built here so this is the one
	// file in mobile-native that touches @evener/appwire-client/testing/ at
	// all: that subpath is in-repo test support (AGENTS.md "Importing the
	// AppWire TypeScript package"), and only a test or dev-support file may
	// import it -- this one qualifies by living under src/dev/, the way the
	// web's own analogous fixture (cmd/evener-hub/frontend/src/dev/editorial-preview/) does.
	navigationCapability(): NavigationCapability;
	// Turns ASKING_SESSION_ID's working row into a pending question and
	// returns the evener/navigation/invalidated payload a real hub would send
	// for that change.
	askQuestion(): NavigationInvalidatedPayload;
}

// The working row EVENER_DEMO_FLEET_ASK_AFTER turns into a question: a plain
// local "evener" session with no pin category, subagents or running job, so
// the only thing that changes on the Board is its band.
const ASKING_SESSION_ID = "s-gateway";
const ASKING_PROJECT = SESSIONS.find((raw) => raw.id === ASKING_SESSION_ID)?.project ?? "evener";

export function createDemoFleet(options: DemoFleetOptions = {}): DemoFleet {
	const startupMs = options.now ?? Date.now();
	const offlineHost = options.offlineHost ?? false;
	const clock = options.clock ?? Date.now;
	// Building the fleet from an empty list, rather than special-casing each
	// answer, keeps every count, section and catalog below in step for free:
	// a hub with nothing live just has nothing to filter, page or search over.
	const sessionsList = options.empty ? [] : SESSIONS;
	// Every resource's revision is one ahead of the navigation sequence: both
	// start there and askQuestion advances them together.
	let sequence = 0;
	let answers = fleetAnswers(sessionsList, sequence + 1, startupMs, offlineHost, clock);

	function askQuestion(): NavigationInvalidatedPayload {
		// A negative `ago` puts updated_at at the moment of asking, after
		// startup, the way a real hub stamps a row when its state changes.
		const askedAgo = (startupMs - clock()) / 1000;
		const asked = sessionsList.map((raw) =>
			raw.id === ASKING_SESSION_ID ? { ...raw, state: "question" as const, ago: askedAgo } : raw,
		);
		sequence += 1;
		const revision = sequence + 1;
		answers = fleetAnswers(asked, revision, startupMs, offlineHost, clock);
		// The resources a real hub invalidates for one row's state change
		// (cmd/evener-hub/navigation_service.go): the manifest's counts, both
		// Board sections, and the row's project. Search has no invalidation
		// target; the next search simply answers from the changed fleet.
		return {
			generationId: DEMO_FLEET_GENERATION,
			sequence,
			targets: [
				{ kind: "manifest", revision },
				{ kind: "section", section: "live", revision },
				{ kind: "section", section: "needs_you", revision },
				{ kind: "project", projectKey: ASKING_PROJECT, revision },
			],
		};
	}

	return {
		answerNavigationRead: (params) => answers.answerNavigationRead(params),
		answerSearch: (params) => answers.answerSearch(params),
		answerAuthList: () => answers.answerAuthList(),
		answerPluginList: () => answers.answerPluginList(),
		navigationCapability: () => ({ ...capability(DEMO_FLEET_GENERATION), sequence }),
		askQuestion,
	};
}

// Every answer the fleet gives, for one fixed list of sessions at one
// revision; askQuestion swaps in a new one rather than mutating this.
function fleetAnswers(
	sessionsList: RawSession[],
	revision: number,
	startupMs: number,
	offlineHost: boolean,
	clock: () => number,
): FleetAnswers {
	const rowById = new Map(sessionsList.map((raw) => [raw.id, toRow(raw, startupMs, offlineHost)]));
	const rowOf = (raw: RawSession) => rowById.get(raw.id) as NavigationSessionSummary;

	const liveRaw = sessionsList.filter((raw) => raw.state !== "shutdown");
	const liveSessions = liveRaw.map(rowOf);
	const needsYouSessions = liveRaw.filter((raw) => NEEDS_YOU_STATES.has(raw.state)).map(rowOf);
	// "9 working" (spec 7.1's Live summary line) is the working band itself,
	// which excludes the approval row despite its sharing state "active".
	const workingCount = sessionsList.filter((raw) => raw.state === "working").length;
	const erroredCount = sessionsList.filter((raw) => raw.state === "failed").length;

	const sources: Source[] = [
		{ id: "local", label: "this host", kind: "local", online: true },
		{ id: "paradise-park", label: "paradise-park", kind: "appwire", online: !offlineHost },
	];

	const pinCategoryIds = ["release", "research"] as const;
	const pinSessions = (id: string) => liveRaw.filter((raw) => raw.category === id).map(rowOf);
	// A category with nothing pinned in it doesn't get a row, which is what
	// empties the pin catalog for the empty fleet. The real hub keeps empty
	// durable sections (navigation_projection.go buildPinSectionsContext);
	// both demo categories hold rows in the full fleet, so only the empty
	// fleet sees the difference.
	const pinSections = pinCategoryIds
		.map((id) => ({ id, name: id === "release" ? "Release" : "Research", count: pinSessions(id).length }))
		.filter((section) => section.count > 0);

	// PROJECT_META is static fixture metadata, not derived from sessionsList
	// (the full fleet lists "home" with no sessions), so an empty fleet needs
	// its own check here rather than falling out of a filter for free.
	const projectKeys = sessionsList.length > 0 ? PROJECT_META.map((project) => project.key) : [];
	const projects = projectKeys.map((key) => projectSummary(sessionsList, key, projectSessionsRaw(sessionsList, key).length));
	const archivedRaw = sessionsList.filter((raw) => raw.archived);
	const archivedProjects: NavigationProjectSummary[] =
		archivedRaw.length > 0 ? [projectSummary(sessionsList, "evener", archivedRaw.length)] : [];
	const testRunRaw = sessionsList.filter((raw) => raw.test);
	const testRunProjects: NavigationProjectSummary[] =
		testRunRaw.length > 0 ? [{ key: "hub-test-env", name: "hub-test-env", session_count: testRunRaw.length }] : [];

	function knownProjectKey(params: NavigationReadParams): string {
		const projectKey = params.projectKey as string;
		if (!projectKeys.includes(projectKey) && projectKey !== "hub-test-env")
			throw new Error(`Unknown demonstration project: ${projectKey}`);
		return projectKey;
	}

	// Every tier is a real, fully pageable list -- including archived, now
	// that SESSIONS carries all 271 (5 named plus the generated filler) --
	// so callers page it with the same page() every other resource uses
	// instead of a bespoke "5 rows, 266 remaining forever" shortcut.
	function tierRows(projectKey: string, tier: "current" | "recent" | "archived"): NavigationSessionSummary[] {
		if (tier === "archived") return projectKey === "evener" ? archivedRaw.map(rowOf) : [];
		if (projectKey === "hub-test-env") return tier === "current" ? testRunRaw.map(rowOf) : [];
		const inProject = projectSessionsRaw(sessionsList, projectKey);
		const inTier = tier === "current" ? inProject.filter((raw) => raw.ago < D) : inProject.filter((raw) => raw.ago >= D);
		return inTier.map(rowOf);
	}

	// A value the wire's own uint32 field could actually carry: a safe integer
	// (so a float that already lost integer precision, like a MAX_SAFE_INTEGER
	// overflow, is refused) no greater than 2**32-1.
	function fitsWireUint32(value: number): boolean {
		return Number.isSafeInteger(value) && value <= 0xffffffff;
	}

	// Mirrors cmd/evener-hub/app_navigation.go's navigationReadPage: offset and
	// limit are validated, never clamped -- a negative offset (the wire's own
	// uint32 field can't even decode one), a zero or negative limit, or a
	// limit over the resource's own maximum are all hard errors, the same as
	// an unrecognized resource elsewhere in this file. The wire's uint32 also
	// can't carry a fractional, NaN, or out-of-range value, so both are
	// required to fit it here too -- otherwise they'd pass the comparisons
	// below (NaN fails every relational operator, and an out-of-range integer
	// would still slice and subtract as if it were valid) and produce a
	// truncated page or a nonsensical remaining count instead of an error.
	function page<T>(items: T[], params: NavigationReadParams, maximum: number): { page: T[]; remaining: number } {
		const offset = params.offset ?? 0;
		if (!fitsWireUint32(offset)) throw new Error(`offset must be an integer the wire can carry: ${offset}`);
		if (offset < 0) throw new Error(`offset must not be negative: ${offset}`);
		let limit = maximum;
		if (params.limit !== undefined) {
			if (!fitsWireUint32(params.limit)) throw new Error(`limit must be an integer the wire can carry: ${params.limit}`);
			if (params.limit <= 0) throw new Error("limit must be greater than zero");
			if (params.limit > maximum) throw new Error(`limit exceeds maximum of ${maximum}`);
			limit = params.limit;
		}
		return { page: items.slice(offset, offset + limit), remaining: Math.max(0, items.length - (offset + limit)) };
	}

	// cmd/evener-hub/navigation_projection.go sets a resource's Truncated
	// whenever any row it projected (at any depth) had its own children
	// capped (OmittedDescendants set) -- not only when the page itself was
	// cut short. Mirrored here instead of a hardcoded false.
	function anyTruncated(rows: NavigationSessionSummary[]): boolean {
		return rows.some((row) => (row.omitted_descendants ?? 0) > 0 || anyTruncated(row.children));
	}

	function answerNavigationRead(params: NavigationReadParams): NavigationReadResponse {
		switch (params.resource) {
			case "manifest":
				return respond(revision, params, {
					sources,
					attentionSummary: { needsYou: needsYouSessions.length, error: erroredCount, working: workingCount },
					sections: {
						live: { count: liveSessions.length },
						needs_you: { count: needsYouSessions.length },
						pin_sections: { count: pinSections.length },
					},
					catalogs: {
						projects: { count: projects.length },
						archived_projects: { count: archivedProjects.length },
						test_runs: { count: testRunProjects.length },
					},
				});
			case "section": {
				// cmd/evener-hub/app_navigation.go's navigationReadKeyWithFields:
				// an unrecognized section is a hard error, never a fallback to Live.
				if (params.section !== "live" && params.section !== "needs_you")
					throw new Error(`Unknown demonstration section: ${params.section}`);
				const source = params.section === "needs_you" ? needsYouSessions : liveSessions;
				const { page: sessions, remaining } = page(source, params, NAVIGATION_SECTION_LIMIT);
				return respond(revision, params, { sessions, remaining, truncated: anyTruncated(sessions) });
			}
			case "pin_catalog": {
				const { page: sections, remaining } = page(pinSections, params, NAVIGATION_CATALOG_LIMIT);
				return respond(revision, params, { pin_sections: sections, remaining });
			}
			case "pin_section": {
				const id = pinCategoryIds.find((candidate) => candidate === params.sectionId);
				if (!id) throw new Error(`Unknown demonstration pin section: ${params.sectionId}`);
				const { page: sessions, remaining } = page(pinSessions(id), params, NAVIGATION_SECTION_LIMIT);
				return respond(revision, params, { sessions, remaining, truncated: anyTruncated(sessions) });
			}
			case "catalog": {
				// Same as "section": an unrecognized catalog is a hard error, never
				// a silent fallback to the projects catalog.
				if (params.catalog !== "projects" && params.catalog !== "archived_projects" && params.catalog !== "test_runs")
					throw new Error(`Unknown demonstration catalog: ${params.catalog}`);
				const source = params.catalog === "archived_projects" ? archivedProjects : params.catalog === "test_runs" ? testRunProjects : projects;
				const { page: rows, remaining } = page(source, params, NAVIGATION_CATALOG_LIMIT);
				return respond(revision, params, { projects: rows, remaining });
			}
			case "project": {
				const projectKey = knownProjectKey(params);
				const current = tierRows(projectKey, "current");
				const recent = tierRows(projectKey, "recent");
				// wireV2's "project" branch hardcodes every tier's own remaining to
				// 0 regardless of input (a real project_page read reports the true
				// count instead), so only the preview size shown here is ours to
				// choose: the archived tier is now a real 271-row list, and an
				// overview embedding all of it would defeat "overview". Preview the
				// same 5 rows this call showed before archived became fully
				// pageable; "See all" is what project_page is for.
				const archived = tierRows(projectKey, "archived").slice(0, PROJECT_OVERVIEW_ARCHIVED_PREVIEW);
				return respond(revision, params, {
					key: projectKey,
					current: { sessions: current, remaining: 0 },
					recent: { sessions: recent, remaining: 0 },
					archived: { sessions: archived, remaining: 0 },
					truncated: anyTruncated([...current, ...recent, ...archived]),
				});
			}
			case "project_page": {
				const projectKey = knownProjectKey(params);
				// Same as section/catalog: an unrecognized tier is a hard error --
				// it must never fall through to being served as "recent".
				if (params.tier !== "current" && params.tier !== "recent" && params.tier !== "archived")
					throw new Error(`Unknown demonstration tier: ${params.tier}`);
				const tier = params.tier;
				const { page: sessions, remaining } = page(tierRows(projectKey, tier), params, NAVIGATION_SECTION_LIMIT);
				return respond(revision, params, { key: projectKey, tier, sessions, remaining, truncated: anyTruncated(sessions) });
			}
			default:
				throw new Error(`Navigation resource not served by the demo fleet: ${params.resource}`);
		}
	}

	function answerSearch(params: SearchParams): SearchResponse {
		const query = params.query?.trim().toLowerCase();
		const matches = query ? sessionsList.filter((raw) => raw.title.toLowerCase().includes(query)) : sessionsList;
		// Sampled per call, not the startup instant: a hit's age should grow as
		// the demo hub keeps running, the same as a real search would.
		const now = clock();
		const toHit = (raw: RawSession) => {
			const row = rowOf(raw);
			// relativeAge answers undefined for a row with no timestamp; a
			// SearchResult's age is a required string, so a missing one reads
			// "now", the way this file's own copy used to.
			const age = relativeAge(row.updated_at, now) ?? "now";
			return {
				id: row.session_id,
				title: row.title,
				project: row.project,
				state: row.state,
				age,
				ref: row.ref,
				// The same flags a navigation row carries (#2583): a past (ended)
				// row never has either set, so this needs no shutdown special case.
				...(row.ask_pending ? { askPending: true as const } : {}),
				...(row.approval_pending ? { approvalPending: true as const } : {}),
			};
		};
		return {
			live: matches.filter((raw) => raw.state !== "shutdown").map(toHit),
			past: matches.filter((raw) => raw.state === "shutdown").map(toHit),
		};
	}

	function answerAuthList(): AuthListResponse {
		return {
			providers: [
				{
					provider: EXPIRED_PROVIDER,
					supported: true,
					signedIn: false,
					// cmd/evener-hub/app_auth.go's openAIInstanceStatusKeyed: the Codex
					// transport's ActiveSource is "oauth" whenever an OAuth record
					// exists at all (HasStoredOAuth true), expired or not -- "none" is
					// only for a provider with no record. This is an expired record.
					activeSource: "oauth",
					hasStoredOAuth: true,
					needsLogin: true,
				},
			],
		};
	}

	function answerPluginList(): PluginListResponse {
		return {
			plugins: PLUGINS.map((plugin) => ({
				plugin: plugin.id,
				marketplace: plugin.mp,
				version: plugin.version,
				enabled: plugin.on,
				autoUpgrade: false,
				broken: false,
				installPath: `~/.claude/plugins/${plugin.mp}/${plugin.id}`,
				installedAt: startupMs - 30 * D * 1000,
				lastUpdated: startupMs - 1 * D * 1000,
			})),
		};
	}

	return { answerNavigationRead, answerSearch, answerAuthList, answerPluginList };
}
