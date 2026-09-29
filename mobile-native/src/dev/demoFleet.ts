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
	ArchiveParams,
	ArchiveResponse,
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
import { type DemoCoordinator, type DemoSubagent, demoActivityTree } from "./demoSubagents.js";

// The generation id the fleet's navigationCapability advertises in demo-hub.mts's
// initialize handshake. Every wireV2 response must carry the exact same id:
// the shared navigation store rejects any other generation as a mismatch
// (appwire-client/typescript/state/navigation/revalidator.ts's `validate`,
// "generation mismatch"). Exported for the tests that assert it.
export const DEMO_FLEET_GENERATION = "demo-fleet";
// Every resource shares one revision, bumped when the fleet changes (the
// steps below play, or an archive), so an invalidation's
// target revision is one the next read actually reaches: the navigation
// store refuses a response below the revision it was told to expect.
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

// The base62 alphabet the hub's own ids use
// (appwire-client/typescript/entityIds.ts, identifier/uuid.go). A real hub
// names a session with a 22-character id of exactly these, and a `local:` ref
// is recognised only when its id is that shape
// (mobile-native/src/sessionDeletionResult.ts's localSessionId). The Board's
// archiveTarget reads a local row's identity through that same check, so a
// session the demo fleet names with a readable slug (s-gateway, ...) must
// still carry a real-shaped id on the wire.
const SESSION_ID_ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

// The 22-character wire id a fleet session with this slug carries. Derived
// from the slug so it is stable (a slug always yields the same id) and spread
// across the whole id space so no two fleet rows share one. The slug stays the
// fixture's own key -- and the handle the tests name -- while only the wire's
// ref and session_id carry this id.
export function demoSessionId(slug: string): string {
	// FNV-1a seeds a xorshift32 generator. A nonzero state keeps the generator
	// periodic, and because xorshift32 is a bijection distinct seeds yield
	// distinct sequences, so distinct slugs collide only if their hashes do --
	// which the fleet's own uniqueness test rules out for every slug it names.
	let state = 0x811c9dc5;
	for (let i = 0; i < slug.length; i++) {
		state = Math.imul(state ^ slug.charCodeAt(i), 0x01000193) >>> 0;
	}
	if (state === 0) state = 0x9e3779b9;
	let id = "";
	for (let i = 0; i < 22; i++) {
		state ^= state << 13;
		state >>>= 0;
		state ^= state >>> 17;
		state ^= state << 5;
		state >>>= 0;
		id += SESSION_ID_ALPHABET[state % SESSION_ID_ALPHABET.length];
	}
	return id;
}

// Names cycled through for a swarm whose members aren't individually named in
// the fixture, copied verbatim from data.js's swarmNames.
const SWARM_NAMES = [
	"Reproduce TestRetirementTreeSettle locally",
	"Bisect settle ordering change",
	"Check drain ordering in tests",
	"Audit delegate settle callers",
	"Run agent tests under -race",
	"Run hubcore tests under -race",
	"Check appwire projector ordering",
	"Stabilize TestFoldPublicationMarkers",
	"Trace retirement drain wakeups",
	"Verify job tree revisions",
	"Run cmd/evener-hub browser guards",
	"Check interrupt marker ownership",
	"Explore retirement drain callers",
	"Review settle lock scope",
	"Run linux -race on agent",
	"Check TestQueueRetirement flake",
	"Read CI logs for run 34717544502",
	"Compare failing seeds",
	"Check compaction fold timing",
	"Audit watch timer shutdown",
	"Run jobstore descendant merge tests",
	"Check delegate attention flags",
	"Stabilize TestTranscriptPaging",
	"Probe navigation invalidation order",
	"Run make lint on changed modules",
	"Check go vet windows tags",
	"Replay failing job trees",
	"Measure settle pass duration",
	"Check restart continuity path",
	"Verify cancel queued on retire",
	"Run hub relay tests",
	"Inspect secret-scan output",
	"Check stacked merge duplication",
	"Run appwire codec tests",
	"Verify ask pending clears",
	"Run TUI transcript suite",
	"Check mutation outbox replay",
	"Run native shared-session tests",
	"Stabilize TestRelayCloseFrame",
	"Check projector item paging",
	"Run fuzz smoke on tool args",
	"Verify steering injection order",
	"Check delegate budget accounting",
	"Run workspace isolation tests",
	"Verify lane branch disposal",
	"Check CI cache keys",
	"Run makefile audits",
	"Check TestHostAttachRetry",
	"Verify session namer fallback",
	"Run SDK package qualification",
	"Check provider retry caps",
	"Run docs freshness check",
];

type ProtoHost = "magic-kingdom" | "paradise-park";
// The prototype's own state vocabulary (data.js sessions[].state), mapped to
// the wire's below. "shutdown" covers every non-live session (shut down,
// test-run and archived alike -- data.js sets `live: false` on all of them).
export type ProtoState = "failed" | "question" | "approval" | "restart" | "yourmove" | "working" | "idle" | "shutdown";
type SubState = RawSubagent["state"];

// A subagent as data.js's swarms name it, with the detail the Subagents
// list's rows show (demoSubagents.ts).
export type RawSubagent = DemoSubagent;

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
	model?: string; // data.js's session model, for a coordinator's subagent tree
}

// data.js's swarm for s-pr2138: two named failures plus 31 running, 3 waiting
// and 18 done generated ones (i in the same ranges, same modulo arithmetic).
const PR2138_CHILDREN: RawSubagent[] = [
	{
		id: "g-settle",
		title: "Fix race in tree settle",
		state: "failed",
		model: "glm-5.3-vision",
		lane: "fix-settle-race",
		ago: 6 * M,
		elapsed: 21 * M,
		tokens: "1.2M",
		line: "Failed: go test exited 1 (3 times)",
		children: [
			{
				id: "g-settle-1",
				title: "Check drain ordering in tests",
				state: "running",
				model: "deepseek-4.1-flash",
				ago: 20,
				elapsed: 4 * M,
				tokens: "210K",
				line: "Reading agent/retirement_test.go",
			},
		],
	},
	{
		id: "g-repro",
		title: "Reproduce TestRetirementTreeSettleDrains",
		state: "failed",
		model: "deepseek-4.1-flash",
		ago: 9 * M,
		elapsed: 14 * M,
		tokens: "640K",
		line: "Failed: could not reproduce in 200 runs",
	},
	...Array.from({ length: 31 }, (_, i) => ({
		id: `g-run-${i}`,
		title: SWARM_NAMES[(i + 3) % SWARM_NAMES.length] as string,
		state: "running" as const,
		model: i % 4 === 0 ? "glm-5.3-vision" : "deepseek-4.1-flash",
		ago: 5 + ((i * 7) % 90),
		elapsed: (2 + ((i * 5) % 30)) * M,
		tokens: `${120 + ((i * 37) % 900)}K`,
		...(i % 6 === 0 ? { lane: `lane-${i + 1}` } : {}),
		line: [
			"Running go test ./agent/...",
			"Reading agent/delegate_runtime.go",
			"Editing agent/retirement.go",
			"Searching for settleTree",
			"Running go test -race ./internal/hubcore",
			"Thinking",
		][i % 6] as string,
	})),
	...Array.from({ length: 3 }, (_, i) => ({
		id: `g-wait-${i}`,
		title: SWARM_NAMES[(i + 40) % SWARM_NAMES.length] as string,
		state: "done" as const,
		model: "deepseek-4.1-flash",
		ago: (3 + i) * M,
		elapsed: (8 + i) * M,
		tokens: "300K",
		line: "Finished and reported back",
	})),
	...Array.from({ length: 18 }, (_, i) => ({
		id: `g-done-${i}`,
		title: SWARM_NAMES[(i + 20) % SWARM_NAMES.length] as string,
		state: "done" as const,
		model: "deepseek-4.1-flash",
		ago: (10 + i * 2) * M,
		elapsed: (5 + (i % 9)) * M,
		tokens: `${90 + i * 23}K`,
		line: ["Tests pass", "No race found in this path", "Fixed and verified", "Report written"][i % 4] as string,
	})),
];

const RETRY_CHILDREN: RawSubagent[] = [
	{
		id: "r-1",
		title: "Find where retries are scheduled",
		state: "done",
		model: "gpt-5.6",
		ago: 2 * H,
		elapsed: 9 * M,
		tokens: "380K",
		line: "Found the loop in llm/retry.go",
	},
	{
		id: "r-2",
		title: "Write a test for the 429 loop",
		state: "done",
		model: "gpt-5.6",
		ago: 90 * M,
		elapsed: 12 * M,
		tokens: "410K",
		line: "Test reproduces the endless retry",
	},
	{
		id: "r-3",
		title: "Cap retries with backoff",
		state: "failed",
		model: "gpt-5.6",
		ago: 3 * M,
		elapsed: 18 * M,
		tokens: "520K",
		line: "Failed: provider sign-in expired",
	},
	{
		id: "r-4",
		title: "Check other providers for the same loop",
		state: "done",
		model: "gpt-5.6",
		ago: 4 * M,
		elapsed: 6 * M,
		tokens: "150K",
		line: "Finished and reported back",
	},
];

const TASKLIST_CHILDREN: RawSubagent[] = [
	{
		id: "t-1",
		title: "Update TaskCard tests",
		state: "running",
		model: "glm-5.3-vision",
		ago: 4,
		elapsed: 5 * M,
		tokens: "160K",
		line: "Running npm test",
	},
	{
		id: "t-2",
		title: "Update the browser guard",
		state: "running",
		model: "glm-5.3-vision",
		ago: 9,
		elapsed: 4 * M,
		tokens: "120K",
		line: "Editing scripts/layoutguard/run.mjs",
	},
	{
		id: "t-3",
		title: "Check the Tasks panel",
		state: "running",
		model: "deepseek-4.1-flash",
		ago: 7,
		elapsed: 3 * M,
		tokens: "95K",
		line: "Reading TasksPanel.tsx",
	},
	{
		id: "t-4",
		title: "Measure card height",
		state: "done",
		model: "deepseek-4.1-flash",
		ago: 6 * M,
		elapsed: 2 * M,
		tokens: "40K",
		line: "Card is 212pt today; one line is 36pt",
	},
];

const HIER_CHILDREN: RawSubagent[] = [
	{
		id: "h-a",
		title: "Mock layout A: project, then host",
		state: "done",
		model: "glm-5.3-vision",
		ago: 80 * M,
		elapsed: 11 * M,
		tokens: "420K",
		line: "Layout A mock written",
	},
	{
		id: "h-b",
		title: "Mock layout B: host badges in projects",
		state: "done",
		model: "glm-5.3-vision",
		ago: 78 * M,
		elapsed: 12 * M,
		tokens: "460K",
		line: "Layout B mock written",
	},
	{
		id: "h-c",
		title: "Mock layout C: host, then project",
		state: "done",
		model: "glm-5.3-vision",
		ago: 77 * M,
		elapsed: 10 * M,
		tokens: "400K",
		line: "Layout C mock written",
	},
	{
		id: "h-d",
		title: "Count sessions per project and host",
		state: "done",
		model: "deepseek-4.1-flash",
		ago: 2 * H,
		elapsed: 3 * M,
		tokens: "80K",
		line: "88% of sessions are in evener",
	},
	{
		id: "h-e",
		title: "Review the three mocks",
		state: "done",
		model: "gpt-5.6",
		ago: 70 * M,
		elapsed: 6 * M,
		tokens: "210K",
		line: "B wins on scan time",
	},
	{
		id: "h-f",
		title: "Write the plan",
		state: "done",
		model: "glm-5.3-vision",
		ago: 65 * M,
		elapsed: 5 * M,
		tokens: "130K",
		line: "Plan written",
	},
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
	for (let i = 0; i < counts.fail; i++)
		out.push({ id: `${prefix}${k}`, title: pick(k++), state: "failed", ago: 20 * M });
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
		model: "gpt-5.6",
		title: "Fix Endless Provider Retry Loop",
		host: "paradise-park",
		state: "failed",
		ago: 2 * M,
		children: RETRY_CHILDREN,
	},
	{
		id: "s-audit",
		title: "Audit Tool Descriptions for Implied Options",
		state: "question",
		ago: 14 * M,
		subs: { run: 0, fail: 0, done: 8 },
	},
	{
		id: "s-mirror",
		title: "Mirror Docs Site Locally",
		project: "prime-radiant-inc.github.io",
		state: "approval",
		ago: 21 * M,
	},
	{ id: "s-namer", title: "Tune Session Namer Token Cap", state: "restart", ago: 47 * M },

	// Finished, not yet seen (4)
	{
		id: "s-hier",
		title: "Host Project Hierarchy UI Mockups",
		state: "yourmove",
		model: "glm-5.3-vision",
		ago: 62 * M,
		children: HIER_CHILDREN,
	},
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
		model: "glm-5.3-vision",
		title: "Get PR 2138 Test Clean",
		state: "working",
		ago: 5,
		category: "release",
		children: PR2138_CHILDREN,
		activity: "Waiting on 31 subagents",
	},
	{
		id: "s-tasklist",
		model: "glm-5.3-vision",
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
	{
		id: "s-resume",
		title: "Fix Missing Prompt on Session Resume",
		state: "working",
		ago: 4,
		activity: "Reading agent/session_resume.go",
	},
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
		// package-import-paths-check.sh flags a quoted relative path into the
		// AppWire package ("./" or "../"), and this fixture string names no
		// such path.
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
	{
		id: "s-house",
		title: "Thermostat Schedule Script",
		host: "paradise-park",
		project: "house",
		state: "shutdown",
		ago: 4 * D,
	},
	{ id: "s-copy", title: "Tighten Pricing Page Copy", project: "copy-writing", state: "shutdown", ago: 5 * D },
	{
		id: "s-wasm2",
		title: "WASM Linker Symbol Clash",
		host: "paradise-park",
		project: "c-to-wasm",
		state: "shutdown",
		ago: 2 * D,
	},

	// Test runs (3)
	{
		id: "s-test1",
		title: "Live Stack Smoke: Session on Host",
		project: "hub-test-env",
		state: "shutdown",
		ago: 20 * H,
		test: true,
	},
	{
		id: "s-test2",
		title: "Retirement Browser Guard Run",
		project: "hub-test-env",
		state: "shutdown",
		ago: 26 * H,
		test: true,
	},
	{
		id: "s-test3",
		title: "Spawn Guard Fixture Session",
		project: "hub-test-env",
		state: "shutdown",
		ago: 30 * H,
		test: true,
	},

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
export const PROJECT_META: { key: string; workingDir: string }[] = [
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
export const PLUGINS: { id: string; mp: string; on: boolean; version: string }[] = [
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
export const EXPIRED_PROVIDER = "codex-jesse-fsck.com";

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
	// A subagent is a session like any other, named by a hub-shaped id; its
	// parent's delegates name its transcript by this same ref (demoSessions.ts).
	const sessionId = demoSessionId(sub.id);
	return {
		ref: hostSessionRef(ownerHostId, sub.id),
		host_id: ownerHostId,
		session_id: sessionId,
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

// A live root's whole-tree subagent tally (S3), as its daemon counts it: every
// depth, not just the capped rows its children keep. The demo still nests its
// subagent rows under children for the tree views, but the Board's chip and
// working why line read this tally, not those rows.
function subagentTally(subs: readonly RawSubagent[]): { running: number; failed: number; done: number } {
	const tally = { running: 0, failed: 0, done: 0 };
	for (const sub of subs) {
		tally[sub.state] += 1;
		const nested = subagentTally(sub.children ?? []);
		tally.running += nested.running;
		tally.failed += nested.failed;
		tally.done += nested.done;
	}
	return tally;
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

// The project a fleet session belongs to; the fixture leaves evener's unset.
function projectKeyOf(raw: { project?: string }): string {
	return raw.project ?? "evener";
}

// The ref the wire names a session or subagent by: the host that owns it and
// the hub-shaped id of its fixture slug.
export function hostSessionRef(host: string, slug: string): string {
	return `${host}:${demoSessionId(slug)}`;
}

// The ref the wire names a fleet session by: its owning host and its id.
function sessionRef(raw: RawSession): string {
	return hostSessionRef(hostId(raw.host), raw.id);
}

// A fleet session as demoSessions.ts needs it to serve the session's own
// thread/read: the row's identity and state, where it runs, and its subagent
// tree, uncapped (a session's own delegates are not a navigation list).
export interface FleetSession {
	slug: string;
	ref: string;
	hostId: string;
	title: string;
	state: ProtoState;
	workingDir: string;
	ago: number;
	activity?: string;
	subagents: RawSubagent[];
	/** A subagent's own session: when its run started (demoRunStartedAt), which
	 * its working time counts from. */
	runStartedAt?: number;
}

// The ref the fleet names a session by, from its fixture slug.
// Not in the Board mockup, so served only with EVENER_DEMO_FLEET_TOOLS: a
// session whose transcript replays the recorded wire corpora, one step of
// every tool family (demoToolFamilies.ts).
const TOOL_FAMILIES_SESSION: RawSession = {
	id: "s-tools",
	title: "Show Every Tool Family",
	state: "yourmove",
	ago: 4 * H,
};

/** The fleet's sessions, with the tool families session when asked for. */
function fleetList(toolFamilies = false): RawSession[] {
	return toolFamilies ? [...SESSIONS, TOOL_FAMILIES_SESSION] : SESSIONS;
}

export function fleetSessionRef(slug: string): string {
	const raw = fleetList(true).find((candidate) => candidate.id === slug);
	if (!raw) throw new Error(`Unknown demonstration session: ${slug}`);
	return sessionRef(raw);
}

// Every session the fleet holds, in the fleet's own order.
export function fleetSessions(toolFamilies = false): FleetSession[] {
	return fleetList(toolFamilies).map((raw) => {
		const projectKey = projectKeyOf(raw);
		return {
			slug: raw.id,
			ref: sessionRef(raw),
			hostId: hostId(raw.host),
			title: raw.title,
			state: raw.state,
			// hub-test-env is the one project PROJECT_META leaves without a folder.
			workingDir:
				PROJECT_META.find((project) => project.key === projectKey)?.workingDir ?? `/home/jesse/git/${projectKey}`,
			ago: raw.ago,
			...(raw.activity ? { activity: raw.activity } : {}),
			subagents: rawChildren(raw),
		};
	});
}

// The plugins a fleet session starts with: data.js's defaultPlugins, every
// enabled one.
export function enabledPluginNames(): string[] {
	return PLUGINS.filter((plugin) => plugin.on).map((plugin) => plugin.id);
}

function toRow(raw: RawSession, startupMs: number, offlineHost: boolean): NavigationSessionSummary {
	const owner = hostId(raw.host);
	const project = projectKeyOf(raw);
	const sessionId = demoSessionId(raw.id);
	const { state, askPending, approvalPending } = WIRE_STATE[raw.state];
	const live = raw.state !== "shutdown";
	const offline = owner === "paradise-park" && offlineHost;
	const subs = rawChildren(raw);
	const { capped, omitted } = capChildren(subs);
	const jobs = runningJobs(raw);
	return {
		ref: sessionRef(raw),
		host_id: owner,
		session_id: sessionId,
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
		...(live && subs.length > 0 ? { subagents: subagentTally(subs) } : {}),
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
	return sessions.filter((raw) => underProjects(raw) && projectKeyOf(raw) === projectKey);
}

// The hosts that own a project's sessions, in the hub's own shape
// (NavigationProjectSummary.sources): "local" for this hub's sessions and a
// host's name for its own, omitted when this hub owns every one.
function projectSources(sessions: RawSession[], projectKey: string): string[] | undefined {
	const owners = new Set(sessions.filter((raw) => projectKeyOf(raw) === projectKey).map((raw) => hostId(raw.host)));
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
	// sessions, so the manifest, sections, project catalogs and search all come
	// back empty. The pin catalog still names both durable pin sections, each
	// with count 0, as the real hub keeps them. The sources still name a host
	// (a real empty hub is still a hub), so only offlineHost touches them. For
	// the Board's EmptyBoard state.
	empty?: boolean;
	// Mirrors EVENER_DEMO_FLEET_ASK_AFTER: that many seconds after the demo
	// hub starts, the working row s-gateway ("Design Gateway Token Command
	// MVP") asks a question, moving from Working into Needs you, so the
	// Board's spring and amber wash can be watched on a still list. The fleet
	// itself only knows how to make the change (step("question")); demo-hub.mts
	// owns the timer and sends the resulting invalidation, so a test fires the
	// change by calling step("question") directly instead of sleeping.
	askAfterSeconds?: number;
	// Mirrors EVENER_DEMO_FLEET_PLAN_REVISED: demo-hub.mts serves the
	// settle-race plan's revision rather than its first text, so a Reader that
	// read the plan before the restart shows what changed (frame 17).
	planRevised?: boolean;
	// Mirrors EVENER_DEMO_FLEET_OLDER: demo-hub.mts puts fifteen older turns
	// ahead of s-pr2138's and pages them by item (dev/demoOlderHistory.ts).
	olderHistory?: boolean;
	// Mirrors EVENER_DEMO_LONG: demo-hub.mts serves the sessions' long content
	// (demoSessions.ts LONG_CONTENT) in place of the usual.
	long?: boolean;
	// Mirrors EVENER_DEMO_FLEET_TOOLS: adds "Show Every Tool Family" to
	// Finished, a session replaying the recorded wire corpora.
	toolFamilies?: boolean;
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
	// Plays one of the prototype's scripted events (sim.js's `events`) and
	// returns the evener/navigation/invalidated payload a real hub would send
	// for that change. "question" turns ASKING_SESSION_ID's working row into
	// a pending question.
	step(name: DemoStep): NavigationInvalidatedPayload;
	// Moves the session named by its wire `ref` to `state` and returns the
	// evener/navigation/invalidated payload for that row change, or null when
	// the fleet doesn't hold the session or its row is already in that state.
	// demo-hub.mts calls this when a fleet session's turn starts or stops, so
	// the Board's row follows the session's thread.
	setSessionState(ref: string, state: ProtoState): NavigationInvalidatedPayload | null;
	// Answers evener/archive/set for a session, named as the Board's
	// archiveTarget names it: a local row by its session id (or its local:
	// ref), another host's by its ref. Returns the reply and the
	// evener/navigation/invalidated payload a real hub sends for it.
	archive(params: ArchiveParams): { response: ArchiveResponse; invalidated: NavigationInvalidatedPayload };
	// Answers evener/jobs/list: a coordinator's subagent tree, and an empty
	// root for any other fleet session (demoSubagents.ts).
	answerJobsList(params: { ref?: string; continuation?: string }): { data: unknown };
}

// The working row EVENER_DEMO_FLEET_ASK_AFTER turns into a question: a plain
// local "evener" session with no pin category, subagents or running job, so
// the only thing that changes on the Board is its band.
export const ASKING_SESSION_ID = "s-gateway";

/** The prototype's scripted events (sim.js's `events`), for the phase 6
 * alert screenshots: a row changing state, or paradise-park going offline
 * and back (what EVENER_DEMO_FLEET_OFFLINE_HOST sets at startup). */
export type DemoStep = "question" | "failure" | "approval" | "finish" | "host-offline" | "host-online";

// The row each row step changes, and the state it moves to. An approval
// stays "active" on the wire and joins needs_you, as the hub promotes an
// escalation; a finish is a turn ending with the ball in your court.
export const ROW_STEPS: Record<Exclude<DemoStep, "host-offline" | "host-online">, [string, ProtoState]> = {
	question: [ASKING_SESSION_ID, "question"],
	failure: ["s-readintent", "failed"],
	approval: ["s-landing", "approval"],
	finish: ["s-resume", "yourmove"],
};

export function createDemoFleet(options: DemoFleetOptions = {}): DemoFleet {
	const startupMs = options.now ?? Date.now();
	let offlineHost = options.offlineHost ?? false;
	const clock = options.clock ?? Date.now;
	// Building the fleet from an empty list, rather than special-casing each
	// answer, keeps every count, section and catalog below in step for free:
	// a hub with nothing live just has nothing to filter, page or search over.
	// The fleet as it stands; each change replaces it with a changed copy.
	let sessionsList = options.empty ? [] : fleetList(options.toolFamilies);
	// Every resource's revision is one ahead of the navigation sequence: both
	// start there and each change advances them together.
	let sequence = 0;
	let answers = fleetAnswers(sessionsList, sequence + 1, startupMs, offlineHost, clock);

	// Moves the fleet to `changed` at the next sequence and revision, and
	// returns the evener/navigation/invalidated payload for the targets the
	// change names at that revision.
	function commit(
		changed: RawSession[],
		targets: (revision: number) => NavigationInvalidatedPayload["targets"],
	): NavigationInvalidatedPayload {
		sessionsList = changed;
		sequence += 1;
		const revision = sequence + 1;
		answers = fleetAnswers(sessionsList, revision, startupMs, offlineHost, clock);
		return { generationId: DEMO_FLEET_GENERATION, sequence, targets: targets(revision) };
	}

	// Moves `target`'s row to `state` at the next revision and returns the
	// invalidation for one row's state change. A negative `ago` puts updated_at
	// at the moment of the change, after startup, the way a real hub stamps a
	// row when its state changes.
	function commitRowState(target: RawSession, state: ProtoState): NavigationInvalidatedPayload {
		const changedAgo = (startupMs - clock()) / 1000;
		const changed = sessionsList.map((raw) => (raw === target ? { ...raw, state, ago: changedAgo } : raw));
		// The resources a real hub invalidates for one row's state change
		// (cmd/evener-hub/navigation_service.go's commitTargetsLocked): the
		// manifest's counts, both Board sections, the row's own pin section when
		// it sits in one (its rows' state is part of the section's fingerprint),
		// and the row's project. Search has no invalidation target; the next
		// search simply answers from the changed fleet.
		return commit(changed, (revision) => [
			{ kind: "manifest", revision },
			{ kind: "section", section: "live", revision },
			{ kind: "section", section: "needs_you", revision },
			...(target.category ? [{ kind: "pin_section" as const, sectionId: target.category, revision }] : []),
			{ kind: "project", projectKey: projectKeyOf(target), revision },
		]);
	}

	function step(name: DemoStep): NavigationInvalidatedPayload {
		if (name === "host-offline" || name === "host-online") {
			offlineHost = name === "host-offline";
			// The manifest carries the source's online flag, and every section
			// its rows' offline marks.
			return commit(sessionsList, (revision) => [
				{ kind: "manifest", revision },
				{ kind: "section", section: "live", revision },
				{ kind: "section", section: "needs_you", revision },
			]);
		}
		const [id, state] = ROW_STEPS[name];
		const target = sessionsList.find((raw) => raw.id === id);
		// A fleet without the row (EVENER_DEMO_FLEET_EMPTY) has nothing to
		// change, so the step changes nothing: no new sequence, no targets.
		if (!target) return { generationId: DEMO_FLEET_GENERATION, sequence, targets: [] };
		return commitRowState(target, state);
	}

	function archive(params: ArchiveParams): { response: ArchiveResponse; invalidated: NavigationInvalidatedPayload } {
		if (params.kind !== "session") throw new Error("The demo fleet archives sessions only");
		// The hub reads a local: ref as the bare session id its rows carry
		// (app_archive.go's NormalizeDecisionSessionID); another host's session
		// is named by its ref.
		const target = sessionsList.find((raw) => {
			return params.id === sessionRef(raw) || (hostId(raw.host) === "local" && params.id === demoSessionId(raw.id));
		});
		if (!target) throw new Error(`Unknown demonstration session: ${params.id}`);
		const changed = sessionsList.map((raw) => (raw === target ? { ...raw, archived: params.archived } : raw));
		// A session archive changes the manifest's counts, the Board's
		// sections, the project catalogs' counts and the row's project, and
		// the real hub adds every loaded project page besides
		// (app_archive.go's AllLoadedProjects hint, appended last by
		// navigation_service.go's commitTargetsLocked). The list is fixed, a
		// superset of the real hub's, which names only resources that changed;
		// the client just re-reads the extra ones.
		const invalidated = commit(changed, (revision) => [
			{ kind: "manifest", revision },
			{ kind: "section", section: "live", revision },
			{ kind: "section", section: "needs_you", revision },
			{ kind: "catalog", catalog: "projects", revision },
			{ kind: "catalog", catalog: "archived_projects", revision },
			{ kind: "project", projectKey: projectKeyOf(target), revision },
			{ kind: "all_loaded_projects" },
		]);
		return {
			response: { ok: true, navigation: { generation_id: DEMO_FLEET_GENERATION, targets: invalidated.targets } },
			invalidated,
		};
	}

	return {
		answerNavigationRead: (params) => answers.answerNavigationRead(params),
		answerSearch: (params) => answers.answerSearch(params),
		answerAuthList: () => answers.answerAuthList(),
		answerPluginList: () => answers.answerPluginList(),
		navigationCapability: () => ({ ...capability(DEMO_FLEET_GENERATION), sequence }),
		step,
		setSessionState: (ref, state) => {
			const target = sessionsList.find((raw) => sessionRef(raw) === ref);
			if (!target || target.state === state) return null;
			return commitRowState(target, state);
		},
		archive,
		answerJobsList: (params) => demoActivityTree(coordinatorFor(sessionsList, params.ref ?? ""), startupMs),
	};
}

// The fleet session or subagent a ref names, as the coordinator of the
// subagents it started (demoSubagents.ts); nobody's, an empty tree. A
// subagent's are its children, named as the Board's child rows and the
// sessions' delegates name them.
function coordinatorFor(sessions: readonly RawSession[], ref: string): DemoCoordinator {
	for (const raw of sessions) {
		const host = hostId(raw.host);
		const subagentRef = (id: string) => hostSessionRef(host, id);
		const model = raw.model ?? "";
		if (sessionRef(raw) === ref) return { ref, title: raw.title, model, subagents: rawChildren(raw), subagentRef };
		const sub = findSubagent(rawChildren(raw), ref, subagentRef);
		if (sub) return { ref, title: sub.title, model: sub.model ?? model, subagents: sub.children ?? [], subagentRef };
	}
	return { ref, title: "", model: "", subagents: [], subagentRef: (id) => id };
}

function findSubagent(
	subagents: readonly RawSubagent[],
	ref: string,
	refOf: (id: string) => string,
): RawSubagent | undefined {
	for (const sub of subagents) {
		if (refOf(sub.id) === ref) return sub;
		const nested = findSubagent(sub.children ?? [], ref, refOf);
		if (nested) return nested;
	}
	return undefined;
}

// Every answer the fleet gives, for one fixed list of sessions at one
// revision; each change swaps in a new one rather than mutating this.
function fleetAnswers(
	sessionsList: RawSession[],
	revision: number,
	startupMs: number,
	offlineHost: boolean,
	clock: () => number,
): FleetAnswers {
	const rowById = new Map(sessionsList.map((raw) => [raw.id, toRow(raw, startupMs, offlineHost)]));
	const rowOf = (raw: RawSession) => rowById.get(raw.id) as NavigationSessionSummary;

	// An archived session leaves Live, whatever its state.
	const liveRaw = sessionsList.filter((raw) => raw.state !== "shutdown" && !raw.archived);
	const liveSessions = liveRaw.map(rowOf);
	const needsYouSessions = liveRaw.filter((raw) => NEEDS_YOU_STATES.has(raw.state)).map(rowOf);
	// "9 working" (spec 7.1's Live summary line) is the working band itself,
	// which excludes the approval row despite its sharing state "active".
	const workingCount = liveRaw.filter((raw) => raw.state === "working").length;
	const erroredCount = liveRaw.filter((raw) => raw.state === "failed").length;

	const sources: Source[] = [
		{ id: "local", label: "this host", kind: "local", online: true },
		{ id: "paradise-park", label: "paradise-park", kind: "appwire", online: !offlineHost },
	];

	const pinCategoryIds = ["release", "research"] as const;
	// A named pin stays reachable when its session is archived, as the real
	// hub's PinCandidates keeps it (cmd/evener-hub/internal/hubcore/tree.go).
	const pinSessions = (id: string) =>
		sessionsList.filter((raw) => raw.state !== "shutdown" && raw.category === id).map(rowOf);
	// Both categories are durable sections the hub always serves, even with no
	// rows pinned in them (navigation_projection.go buildPinSectionsContext
	// keeps every section it is given, memberCount aside), so an empty fleet
	// still reports Release and Research with count 0 rather than no catalog.
	const pinSections = pinCategoryIds.map((id) => ({
		id,
		name: id === "release" ? "Release" : "Research",
		count: pinSessions(id).length,
	}));

	// PROJECT_META is static fixture metadata, not derived from sessionsList
	// (the full fleet lists "home" with no sessions), so an empty fleet needs
	// its own check here rather than falling out of a filter for free.
	const projectKeys = sessionsList.length > 0 ? PROJECT_META.map((project) => project.key) : [];
	const archivedRaw = sessionsList.filter((raw) => raw.archived);
	const archivedIn = (key: string) => archivedRaw.filter((raw) => projectKeyOf(raw) === key);
	// The real hub moves a project whose every session is archived into
	// Archived projects (cmd/evener-hub/internal/hubcore/tree.go's Tree).
	const allArchived = (key: string) => {
		const sessions = sessionsList.filter((raw) => projectKeyOf(raw) === key && !raw.test);
		return sessions.length > 0 && sessions.every((raw) => raw.archived);
	};
	// An active project counts only its unarchived sessions, unlike the real
	// hub's TotalSessionCount, so evener keeps the fixture's count.
	const projects = projectKeys
		.filter((key) => !allArchived(key))
		.map((key) => projectSummary(sessionsList, key, projectSessionsRaw(sessionsList, key).length));
	// evener is the fixture's stand-in: data.js lists its 271 archived
	// sessions as an archived evener row though evener still has live ones.
	// An archived row counts only its archived sessions.
	const archivedProjects: NavigationProjectSummary[] = projectKeys
		.filter((key) => archivedIn(key).length > 0 && (key === "evener" || allArchived(key)))
		.map((key) => projectSummary(sessionsList, key, archivedIn(key).length));
	const testRunRaw = sessionsList.filter((raw) => raw.test);
	const testRunProjects: NavigationProjectSummary[] =
		testRunRaw.length > 0 ? [{ key: "hub-test-env", name: "hub-test-env", session_count: testRunRaw.length }] : [];

	function knownProjectKey(params: NavigationReadParams): string {
		const projectKey = params.projectKey as string;
		if (!projectKeys.includes(projectKey) && projectKey !== "hub-test-env")
			throw new Error(`Unknown demonstration project: ${projectKey}`);
		return projectKey;
	}

	// The tier a row is served under, in one place: the location branch reports
	// this tier and a reveal then asks that exact project_page tier, so the two
	// must come from one computation. An archived row's is "archived", a
	// hub-test-env test-run row's is "current" (test runs are never split by
	// age), every other row's current or recent by the same 24h boundary.
	function servedTier(raw: RawSession): "current" | "recent" | "archived" {
		return raw.archived ? "archived" : raw.test || raw.ago < D ? "current" : "recent";
	}

	// Every tier is a real, fully pageable list -- including archived, now
	// that SESSIONS carries all 271 (5 named plus the generated filler) --
	// so callers page it with the same page() every other resource uses
	// instead of a bespoke "5 rows, 266 remaining forever" shortcut.
	function tierRows(projectKey: string, tier: "current" | "recent" | "archived"): NavigationSessionSummary[] {
		if (tier === "archived") return archivedIn(projectKey).map(rowOf);
		if (projectKey === "hub-test-env") return testRunRaw.filter((raw) => servedTier(raw) === tier).map(rowOf);
		return projectSessionsRaw(sessionsList, projectKey)
			.filter((raw) => servedTier(raw) === tier)
			.map(rowOf);
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
			if (!fitsWireUint32(params.limit))
				throw new Error(`limit must be an integer the wire can carry: ${params.limit}`);
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
				const source =
					params.catalog === "archived_projects"
						? archivedProjects
						: params.catalog === "test_runs"
							? testRunProjects
							: projects;
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
				return respond(revision, params, {
					key: projectKey,
					tier,
					sessions,
					remaining,
					truncated: anyTruncated(sessions),
				});
			}
			case "location": {
				const ref = params.ref as string;
				const raw = sessionsList.find((session) => rowOf(session).ref === ref);
				if (!raw) throw new Error(`Unknown demonstration session location: ${ref}`);
				// The hub's location is a shallow summary (navigation_projection.go's
				// projectShallow), not a row with its descendants: a location resource
				// holds exactly one entity, and projectShallow sets no omitted_descendants
				// (the child cap is a list-row fact, not the summary's), so the capped
				// count is dropped with the children. servedTier is the one place this
				// row's tier is decided, so the reveal that asks this exact tier's
				// project_page cannot drift from what tierRows serves it under.
				const tier = servedTier(raw);
				const { omitted_descendants: _omitted, ...shallow } = rowOf(raw);
				const response = respond(revision, params, {
					session: { ...shallow, children: [] },
					top_level_ref: ref,
					top_level: true,
				});
				// The shared v2 encoder (wireV2) builds a location's metadata with only
				// ref/top_level_ref/top_level; a real hub also names the row's project,
				// tier and pin section, so they are stamped on here, the way the web's
				// own fixture (cmd/evener-hub/frontend/src/dev/editorial-preview/
				// fixture.ts) stamps the top_level it serves.
				const { metadata } = response.data as { metadata: Record<string, unknown> };
				metadata.project_key = projectKeyOf(raw);
				metadata.tier = tier;
				if (raw.category) metadata.pin_section_id = raw.category;
				return response;
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
				// Unix seconds, as the hub sends them (app_plugins.go UnixSeconds).
				installedAt: Math.floor(startupMs / 1000) - 30 * D,
				lastUpdated: Math.floor(startupMs / 1000) - 1 * D,
			})),
		};
	}

	return { answerNavigationRead, answerSearch, answerAuthList, answerPluginList };
}
