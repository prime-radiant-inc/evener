// The redesign's demo fleet, decoded through the real package codec so the
// fixture can never drift from the wire (see demoFleet.ts's header comment
// for what this fixture represents and where it comes from).
import { describe, expect, it } from "vitest";
import type { ArchivedListParams, NavigationReadParams, NavigationSessionSummary } from "@evener/appwire-client";
import { decodeActivityRead, quietState } from "@evener/appwire-client";
import {
	decodeArchivedListSessions,
	decodeNavigationResponse,
	materializeSnapshot,
	NAVIGATION_CATALOG_LIMIT,
	NAVIGATION_SECTION_LIMIT,
	navigationParamsToResourceKey,
} from "@evener/appwire-client/state/navigation";
import { archiveTarget } from "../board/rowActions.js";
import { localSessionId } from "../sessionDeletionResult.js";
import { createDemoFleet, DEMO_FLEET_GENERATION, demoSessionId, fleetSessionRef } from "./demoFleet.js";
import { DEMO_MODEL_NAMES } from "./demoSetup.js";

const STARTUP = Date.parse("2026-09-26T18:00:00.000Z");

// Decodes and materializes one navigation/read response the way a real
// client would, so a test that reads `.sessions`/`.projects` off the result
// is exercising the same schema the wire enforces, not the fixture's own
// internal shape.
function read(fleet: ReturnType<typeof createDemoFleet>, params: NavigationReadParams) {
	const key = navigationParamsToResourceKey(params);
	const response = fleet.answerNavigationRead(params);
	const decoded = decodeNavigationResponse(key, undefined, response);
	if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
	return materializeSnapshot(key, decoded) as Record<string, unknown>;
}

function params(overrides: Partial<NavigationReadParams> & { resource: string }): NavigationReadParams {
	return { representationVersion: 3, offset: 0, limit: 50, ...overrides };
}

function sessionsOf(materialized: Record<string, unknown>): NavigationSessionSummary[] {
	return materialized.sessions as NavigationSessionSummary[];
}

function liveRows(fleet: ReturnType<typeof createDemoFleet>): NavigationSessionSummary[] {
	return sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
}

// The fixture names a session by its readable slug; the wire carries the
// derived id (demoFleet.ts's demoSessionId), so the tests keep naming slugs and
// translate here.
function findRow(rows: NavigationSessionSummary[], slug: string): NavigationSessionSummary {
	const row = rows.find((row) => row.session_id === demoSessionId(slug));
	if (!row) throw new Error(`missing session ${slug} in [${rows.map((r) => r.session_id).join(", ")}]`);
	return row;
}

it("summarizes the same coordinator running job in navigation and typed activity", () => {
	const fleet = createDemoFleet({ now: STARTUP });
	const row = findRow(liveRows(fleet), "s-tasklist");
	const jobs = fleet.answerSessionJobsList({ ref: row.ref });
	expect(jobs.jobs).toHaveLength(row.running_job_count ?? 0);
	expect(jobs.jobs[0]?.command).toBe(row.running_job_command);
	expect(fleet.answerActivityRead({ ref: row.ref }).jobs).toMatchObject({ known: true, total: 1, active: 1 });
});

describe("the tool families session (EVENER_DEMO_FLEET_TOOLS)", () => {
	it("adds Show Every Tool Family to Live, idle, only when asked for", () => {
		const row = findRow(liveRows(createDemoFleet({ now: STARTUP, toolFamilies: true })), "s-tools");
		expect(row).toMatchObject({ title: "Show Every Tool Family" });
		expect(liveRows(createDemoFleet({ now: STARTUP })).map((row) => row.session_id)).not.toContain(
			demoSessionId("s-tools"),
		);
	});
});

// S17: a summary carries its model's display name, for "Show model on Board
// rows"; a session data.js gives no model carries none.
describe("the fleet's model names", () => {
	it("names a session's model by its catalog display name", () => {
		const rows = liveRows(createDemoFleet({ now: STARTUP, modelNames: DEMO_MODEL_NAMES }));
		expect(findRow(rows, "s-pr2138")).toMatchObject({ model_name: "GLM 5.3 Vision" });
		expect(rows.some((row) => row.model_name === undefined)).toBe(true);
	});
});

describe("demo fleet manifest", () => {
	it('reports the Live and needs-you counts the redesign spec\'s Board mockup shows (section 7.1: "Live 20 (4)")', () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const manifest = read(fleet, params({ resource: "manifest" }));
		expect(manifest.sections).toMatchObject({
			live: { count: 20 },
			needs_you: { count: 4 },
			pin_sections: { count: 2 },
		});
		expect(manifest.catalogs).toMatchObject({
			projects: { count: 10 },
			archived_projects: { count: 1 },
			test_runs: { count: 1 },
		});
		// "9 working" per the Live summary line (7.1) excludes the approval row,
		// which shares state "active" with the working band but is not it.
		expect(manifest.attentionSummary).toEqual({ needsYou: 4, error: 1, working: 9 });
	});

	it("carries two sources with paradise-park online by default, offline under the flag", () => {
		const online = read(createDemoFleet({ now: STARTUP }), params({ resource: "manifest" }));
		expect(online.sources).toEqual([
			{ id: "local", label: "this host", kind: "local", online: true },
			{ id: "paradise-park", label: "paradise-park", kind: "appwire", online: true },
		]);
		const offline = read(createDemoFleet({ now: STARTUP, offlineHost: true }), params({ resource: "manifest" }));
		expect(offline.sources).toContainEqual({
			id: "paradise-park",
			label: "paradise-park",
			kind: "appwire",
			online: false,
		});
	});
});

describe("demo session ids", () => {
	// The id is derived, not stored: a slug must always map to the same
	// 22-character base62 id, so the demo hub and the tests that name a slug
	// agree without hardcoding ids at each call site.
	it("derives a stable 22-character id from a slug", () => {
		expect(demoSessionId("s-gateway")).toBe("C1LNmJiisw9budaNQerUF7");
		expect(demoSessionId("s-retry")).toBe("hUCudFPZI9OperXRX4oacL");
		expect(demoSessionId("s-gateway")).not.toBe(demoSessionId("s-retry"));
	});
});

describe("demo fleet live and needs-you sections", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("holds the 20 live top-level sessions Appendix B's fixture actually contains", () => {
		const rows = liveRows(fleet);
		expect(rows).toHaveLength(20);
		expect(new Set(rows.map((row) => row.session_id)).size).toBe(20);
	});

	it("maps the prototype's states the way the brief spells out: failed, question, approval and so on", () => {
		const rows = liveRows(fleet);
		expect(findRow(rows, "s-retry")).toMatchObject({ state: "errored", live: true, host_id: "paradise-park" });
		expect(findRow(rows, "s-audit")).toMatchObject({ state: "awaiting", ask_pending: true });
		expect(findRow(rows, "s-mirror")).toMatchObject({ state: "active" });
		expect(findRow(rows, "s-namer")).toMatchObject({ state: "restartRequired" });
		expect(findRow(rows, "s-hier")).toMatchObject({ state: "idle" }); // a turn that ended without asking rests idle
		expect(findRow(rows, "s-diff")).toMatchObject({ state: "idle" });
		expect(findRow(rows, "s-pr2138")).toMatchObject({ state: "active" });
	});

	it("gives local sessions a local: ref and remote ones a host-prefixed ref", () => {
		const rows = liveRows(fleet);
		expect(findRow(rows, "s-audit").ref).toBe(`local:${demoSessionId("s-audit")}`);
		expect(findRow(rows, "s-retry").ref).toBe(`paradise-park:${demoSessionId("s-retry")}`);
		expect(findRow(rows, "s-retry").host_id).toBe("paradise-park");
	});

	// #2786: the Board's archiveTarget accepts a local row only when
	// localSessionId(row.ref) matches row.session_id, and that check wants a
	// 22-character alphanumeric id. A slug-shaped id meant no demo row ever
	// offered Archive.
	it("gives every session a real-shaped id, so this hub's rows offer Archive", () => {
		const rows = liveRows(fleet);
		for (const row of rows) {
			expect(row.session_id).toMatch(/^[A-Za-z0-9]{22}$/);
		}
		for (const row of rows.filter((row) => row.host_id === "local")) {
			expect(localSessionId(row.ref)).toBe(row.session_id);
		}
		expect(new Set(rows.map((row) => row.session_id)).size).toBe(rows.length);
	});

	it("computes updated_at relative to startup", () => {
		const rows = liveRows(fleet);
		// s-retry: ago 2 minutes.
		expect(findRow(rows, "s-retry").updated_at).toBe(new Date(STARTUP - 2 * 60 * 1000).toISOString());
	});

	it("holds exactly the 4 needs-you rows: the failure, the question, the approval and the restart", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "needs_you" })));
		expect(rows.map((row) => row.session_id).sort()).toEqual(
			["s-audit", "s-mirror", "s-namer", "s-retry"].map(demoSessionId).sort(),
		);
		// Approval rows keep state "active"; the phone infers approval from
		// showing up here, per the task's own background note.
		expect(findRow(rows, "s-mirror").state).toBe("active");
	});

	it("marks every paradise-park row offline under EVENER_DEMO_FLEET_OFFLINE_HOST, never local rows", () => {
		const offlineFleet = createDemoFleet({ now: STARTUP, offlineHost: true });
		const rows = liveRows(offlineFleet);
		expect(findRow(rows, "s-retry").offline).toBe(true); // paradise-park
		expect(findRow(rows, "s-audit").offline).toBeUndefined(); // local
		const defaultRows = liveRows(fleet);
		expect(findRow(defaultRows, "s-retry").offline).toBeUndefined();
	});
});

describe("demo fleet shallow navigation", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("rejects a navigation representation outside version 3", () => {
		for (const representationVersion of [1, 2, 4]) {
			expect(() => fleet.answerNavigationRead(params({ resource: "manifest", representationVersion }))).toThrow();
		}
	});

	it("keeps named and large descendant swarms out of navigation records", () => {
		const live = liveRows(fleet);
		const archived = decodeArchivedListSessions(
			fleet.answerArchivedList({ catalog: "archived_projects", projectKey: "evener" }).sessions,
		);
		for (const row of [...live, ...archived]) {
			expect(row.children).toEqual([]);
			expect(row).not.toHaveProperty("omitted_descendants");
		}
		expect(findRow(live, "s-retry").kind).toBe("session");
		expect(findRow(archived, "s-fuzz").kind).toBe("session");
	});

	it("preserves a live root's compact whole-tree subagent tally", () => {
		const tally = findRow(liveRows(fleet), "s-pr2138").subagents;
		expect(tally).toBeDefined();
		expect((tally?.running ?? 0) + (tally?.failed ?? 0) + (tally?.done ?? 0)).toBe(55);
		expect(tally?.failed).toBe(2);
		expect(tally?.running).toBeGreaterThan(0);
	});
});

describe("demo fleet running jobs", () => {
	it("surfaces a working session's compact shell count and command", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const rows = liveRows(fleet);
		const tasklist = findRow(rows, "s-tasklist");
		expect(tasklist).toMatchObject({ running_job_count: 1, running_job_command: "go test ./cmd/evener-hub/..." });
		expect(tasklist).not.toHaveProperty("running_jobs");
		// "Thinking" and similar activity lines are not commands.
		expect(findRow(rows, "s-gateway").running_job_count ?? 0).toBe(0);
		expect(findRow(rows, "s-gateway").running_job_command).toBeUndefined();
	});
});

describe("demo fleet pin categories", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("matches the Board mockup's RELEASE (2) and Research (1) chips", () => {
		const catalog = read(fleet, params({ resource: "pin_catalog", limit: 100 }));
		expect(catalog.pin_sections).toEqual([
			{ id: "release", name: "Release", count: 2 },
			{ id: "research", name: "Research", count: 1 },
		]);
	});

	it("lists the release category's two sessions", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "pin_section", sectionId: "release" })));
		expect(rows.map((row) => row.session_id).sort()).toEqual(["s-jobdisp", "s-pr2138"].map(demoSessionId).sort());
	});

	it("rejects an unknown pin section instead of returning an empty page silently", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "pin_section", sectionId: "nope" }))).toThrow();
	});
});

describe("demo fleet location", () => {
	const fleet = createDemoFleet({ now: STARTUP });
	// The wire names a row by its derived id (demoFleet.ts's demoSessionId), not
	// its slug, so a location read must name the id the row actually carries.
	const refOf = (hostId: string, slug: string) => `${hostId}:${demoSessionId(slug)}`;

	it("serves a top-level row's location as the shallow summary the archive check reads", () => {
		const location = read(fleet, params({ resource: "location", ref: refOf("local", "s-jobdisp") }));
		expect(location).toMatchObject({
			ref: refOf("local", "s-jobdisp"),
			top_level_ref: refOf("local", "s-jobdisp"),
			top_level: true,
			project_key: "evener",
			tier: "current",
			pin_section_id: "release",
		});
		// A location resource holds exactly one entity: the row itself, with no
		// descendant tree (cmd/evener-hub's projectShallow summary).
		const session = location.session as NavigationSessionSummary;
		expect(session).toMatchObject({
			ref: refOf("local", "s-jobdisp"),
			session_id: demoSessionId("s-jobdisp"),
			host_id: "local",
		});
		expect(session.children).toEqual([]);
	});

	it("reports a remote row's own host-prefixed ref and project", () => {
		const location = read(fleet, params({ resource: "location", ref: refOf("paradise-park", "s-wasm") }));
		expect(location).toMatchObject({
			ref: refOf("paradise-park", "s-wasm"),
			project_key: "c-to-wasm",
			tier: "current",
		});
		expect((location.session as NavigationSessionSummary).host_id).toBe("paradise-park");
	});

	it("reports an archived row's tier as archived, the fact the Board's archive check compares", () => {
		expect(read(fleet, params({ resource: "location", ref: refOf("local", "s-gocache") }))).toMatchObject({
			tier: "archived",
		});
	});

	it("keeps location summaries shallow even for large activity trees", () => {
		// A location names its session without loading its 467-member activity tree.
		const location = read(fleet, params({ resource: "location", ref: refOf("local", "s-fuzz") }));
		const session = location.session as NavigationSessionSummary;
		expect(session.children).toEqual([]);
		expect(session.omitted_descendants).toBeUndefined();
	});

	it("reports an older row as recent, matching the project tier split", () => {
		// s-roster: ago 1d, not archived -- the same boundary tierRows uses.
		expect(read(fleet, params({ resource: "location", ref: refOf("local", "s-roster") }))).toMatchObject({
			tier: "recent",
		});
	});

	it("reports a test-run session under the tier project_page actually serves it", () => {
		// tierRows serves every hub-test-env row under "current", so a location must
		// not derive "recent" from age for s-test2 (26h) or a reveal would ask a
		// project_page tier that returns an empty page (navigationReveal.ts).
		for (const ref of ["s-test1", "s-test2", "s-test3"].map((slug) => refOf("local", slug)))
			expect(read(fleet, params({ resource: "location", ref }))).toMatchObject({ tier: "current" });
	});

	it("leaves pin_section_id off a row that sits in no pin section", () => {
		expect(read(fleet, params({ resource: "location", ref: refOf("local", "s-audit") }))).not.toHaveProperty(
			"pin_section_id",
		);
	});

	it("rejects a ref the fleet doesn't hold, like the hub's own not-found error", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "location", ref: "local:nope" }))).toThrow(/nope/);
	});
});

describe("demo fleet catalogs and projects", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("counts evener's 17 non-archived, non-test sessions in the projects catalog", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "projects", limit: 100 }));
		const projects = catalog.projects as { key: string; session_count: number }[];
		expect(projects.find((project) => project.key === "evener")).toMatchObject({ session_count: 17 });
		expect(projects.map((project) => project.key)).toContain("home");
		expect(projects.find((project) => project.key === "home")).toMatchObject({ session_count: 0 });
	});

	it("names every host a project's sessions sit on, and none for a project on this hub alone", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "projects", limit: 100 }));
		const projects = catalog.projects as { key: string; sources?: string[] }[];
		expect(projects.find((project) => project.key === "evener")?.sources).toEqual(["local", "paradise-park"]);
		const deslop = projects.find((project) => project.key === "deslop");
		expect(deslop).toBeDefined();
		expect(deslop).not.toHaveProperty("sources");
	});

	it("puts the 271-session archived total on evener's archived_projects row", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "archived_projects", limit: 100 }));
		expect(catalog.projects).toEqual([expect.objectContaining({ key: "evener", session_count: 271 })]);
	});

	it("groups the three test-run sessions under the synthetic hub-test-env project", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "test_runs", limit: 100 }));
		expect(catalog.projects).toEqual([expect.objectContaining({ key: "hub-test-env", session_count: 3 })]);
		const page = sessionsOf(
			read(fleet, params({ resource: "project_page", projectKey: "hub-test-env", tier: "current" })),
		);
		expect(page.map((row) => row.session_id).sort()).toEqual(
			["s-test1", "s-test2", "s-test3"].map(demoSessionId).sort(),
		);
	});

	it("splits a project's sessions into today (current) and older (recent) tiers", () => {
		const current = sessionsOf(
			read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "current" })),
		);
		const recent = sessionsOf(read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "recent" })));
		expect(current.map((row) => row.session_id)).toContain(demoSessionId("s-retry")); // ago 2m: today
		expect(recent.map((row) => row.session_id)).toContain(demoSessionId("s-roster")); // ago 1d: recent
		expect(recent.map((row) => row.session_id)).not.toContain(demoSessionId("s-retry"));
	});

	it("rejects an unknown project", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "project", projectKey: "nope" }))).toThrow();
	});

	// cmd/evener-hub/app_navigation.go rejects an unrecognized tier the same
	// way it rejects an unrecognized section or catalog; this fleet used to
	// silently serve anything but "current"/"archived" as "recent".
	it("rejects an unknown tier instead of silently serving it as recent", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "project_page", projectKey: "evener", tier: "nope" })),
		).toThrow(/tier/i);
	});
});

describe("demo fleet search, auth and plugins", () => {
	it("finds live and past sessions by title", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const response = fleet.answerSearch({ query: "wasm" });
		expect(response.live.map((hit) => hit.id)).toEqual([demoSessionId("s-wasm")]);
		expect(response.past.map((hit) => hit.id)).toEqual([demoSessionId("s-wasm2")]);
		expect(response.live[0]).toMatchObject({ project: "c-to-wasm", ref: `paradise-park:${demoSessionId("s-wasm")}` });
	});

	it("computes a hit's age against the current clock, not frozen at fleet creation", () => {
		const fiveMinutes = 5 * 60 * 1000;
		const fleet = createDemoFleet({ now: STARTUP, clock: () => STARTUP + fiveMinutes });
		// s-wasm: ago 12 minutes: updated_at is STARTUP - 12m. Five minutes after
		// creation that's 17m old; frozen at creation it would still read 12m.
		expect(fleet.answerSearch({ query: "wasm" }).live[0]?.age).toBe("17m");
	});

	// #2583: a live result carries the same askPending/approvalPending a
	// navigation row does, so a session blocked on a question or an approval
	// doesn't read as an ordinary working session in search.
	it("carries askPending and approvalPending on live hits, the same as a navigation row", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		// s-audit: state "question" - awaiting an answer, not an escalation.
		const askHit = fleet.answerSearch({ query: "audit" }).live[0];
		expect(askHit).toMatchObject({ id: demoSessionId("s-audit"), askPending: true });
		expect(askHit?.approvalPending).toBeUndefined();

		// s-mirror: state "approval" - blocked on a sandbox escalation, not a
		// question.
		const approvalHit = fleet.answerSearch({ query: "mirror" }).live[0];
		expect(approvalHit).toMatchObject({ id: demoSessionId("s-mirror"), approvalPending: true });
		expect(approvalHit?.askPending).toBeUndefined();

		// s-wasm: ordinary working session - neither flag applies, and the wire's
		// additive contract omits them rather than sending false.
		const idleHit = fleet.answerSearch({ query: "wasm" }).live[0];
		expect(idleHit?.askPending).toBeUndefined();
		expect(idleHit?.approvalPending).toBeUndefined();
	});

	it("reports one provider needing sign-in, in a combination the hub actually produces", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const response = fleet.answerAuthList();
		// cmd/evener-hub/app_auth.go's openAIInstanceStatusKeyed: a stored OAuth
		// record (however expired) makes ActiveSource "oauth" and HasStoredOAuth
		// true together -- "none" is only for a provider with no record at all.
		expect(response.providers).toEqual([
			expect.objectContaining({
				provider: "codex-jesse-fsck.com",
				needsLogin: true,
				activeSource: "oauth",
				hasStoredOAuth: true,
				signedIn: false,
			}),
		]);
	});

	it("lists the fixture's 14 plugins", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const response = fleet.answerPluginList();
		expect(response.plugins).toHaveLength(14);
		expect(response.plugins.find((plugin) => plugin.plugin === "superpowers")).toMatchObject({
			marketplace: "superpowers-marketplace",
			enabled: true,
			version: "6.4.1",
		});
	});

	it("dates each plugin in Unix seconds, as the hub does: installed 30 days ago, updated 1 day ago", () => {
		const [first] = createDemoFleet({ now: STARTUP }).answerPluginList().plugins;
		const startupSeconds = Math.floor(STARTUP / 1000);
		expect(first?.installedAt).toBe(startupSeconds - 30 * 86400);
		expect(first?.lastUpdated).toBe(startupSeconds - 86400);
	});
});

describe("demo fleet error handling", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("fails loudly on a navigation resource name it doesn't serve", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "mystery" }))).toThrow(/mystery/);
	});

	// cmd/evener-hub/app_navigation.go's navigationReadKeyWithFields: an
	// unrecognized section or catalog value is a hard error, not a silent
	// fallback to the first branch.
	it("rejects an unknown section instead of falling back to Live", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "section", section: "nope" }))).toThrow(/section/i);
	});

	it("rejects an unknown catalog instead of falling back to projects", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "catalog", catalog: "nope" }))).toThrow(/catalog/i);
	});
});

describe("demo fleet generation", () => {
	it("advertises the same generation id every response actually carries", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const key = navigationParamsToResourceKey(params({ resource: "manifest" }));
		const decoded = decodeNavigationResponse(
			key,
			undefined,
			fleet.answerNavigationRead(params({ resource: "manifest" })),
		);
		if (decoded.status !== "snapshot") throw new Error(`expected a snapshot, got ${decoded.status}`);
		expect(decoded.version.generationId).toBe(DEMO_FLEET_GENERATION);
	});
});

describe("demo fleet paging", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("pages the Live section in windows of 7, remaining adding up to the true total", () => {
		const first = read(fleet, params({ resource: "section", section: "live", offset: 0, limit: 7 }));
		expect(sessionsOf(first)).toHaveLength(7);
		expect(first.remaining).toBe(13);
		const second = read(fleet, params({ resource: "section", section: "live", offset: 7, limit: 7 }));
		expect(sessionsOf(second)).toHaveLength(7);
		expect(second.remaining).toBe(6);
		const third = read(fleet, params({ resource: "section", section: "live", offset: 14, limit: 7 }));
		expect(sessionsOf(third)).toHaveLength(6);
		expect(third.remaining).toBe(0);
		const seen = new Set(
			[...sessionsOf(first), ...sessionsOf(second), ...sessionsOf(third)].map((row) => row.session_id),
		);
		expect(seen.size).toBe(20);
	});

	it("pages the projects catalog in windows smaller than its size, remaining adding up", () => {
		const projectsOf = (materialized: Record<string, unknown>) => materialized.projects as unknown[];
		const first = read(fleet, params({ resource: "catalog", catalog: "projects", offset: 0, limit: 4 }));
		expect(projectsOf(first)).toHaveLength(4);
		expect(first.remaining).toBe(6);
		const second = read(fleet, params({ resource: "catalog", catalog: "projects", offset: 4, limit: 4 }));
		expect(projectsOf(second)).toHaveLength(4);
		expect(second.remaining).toBe(2);
		const third = read(fleet, params({ resource: "catalog", catalog: "projects", offset: 8, limit: 4 }));
		expect(projectsOf(third)).toHaveLength(2);
		expect(third.remaining).toBe(0);
	});

	// cmd/evener-hub/app_navigation.go's navigationReadPage rejects an
	// out-of-range page request outright rather than clamping it into range;
	// the demo fleet's own page() must match, not silently coerce.
	it("rejects a limit over the section maximum, like the hub's own validation", () => {
		expect(() =>
			fleet.answerNavigationRead(
				params({ resource: "section", section: "live", offset: 0, limit: NAVIGATION_SECTION_LIMIT + 1 }),
			),
		).toThrow(/limit/i);
	});

	it("rejects a limit over the catalog maximum, like the hub's own validation", () => {
		expect(() =>
			fleet.answerNavigationRead(
				params({ resource: "catalog", catalog: "projects", offset: 0, limit: NAVIGATION_CATALOG_LIMIT + 1 }),
			),
		).toThrow(/limit/i);
	});

	it("rejects a limit of zero, like the hub's own validation", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: 0, limit: 0 })),
		).toThrow(/limit/i);
	});

	it("rejects a negative offset, like the hub's own validation", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: -1, limit: 7 })),
		).toThrow(/offset/i);
	});

	// The wire protocol's offset/limit are uint32 fields, so a fractional or
	// NaN value can never arrive that way -- but NavigationReadParams is typed
	// as plain `number`, and these tests build it directly, bypassing the
	// wire. page() must reject what the wire can never produce instead of
	// silently truncating to a partial page or reporting a NaN remaining
	// count.
	it("rejects a fractional offset", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: 1.5, limit: 7 })),
		).toThrow(/offset/i);
	});

	it("rejects a NaN offset", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: Number.NaN, limit: 7 })),
		).toThrow(/offset/i);
	});

	it("rejects a fractional limit", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: 0, limit: 7.5 })),
		).toThrow(/limit/i);
	});

	it("rejects a NaN limit", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: 0, limit: Number.NaN })),
		).toThrow(/limit/i);
	});

	// An integer can still be too big for the wire: its offset/limit fields are
	// uint32, and a value that overflows JS's own safe-integer precision isn't
	// trustworthy either. page() must reject both instead of paging with a
	// value the real hub's wire type could never carry.
	it("rejects an offset past the wire's uint32 range", () => {
		expect(() =>
			fleet.answerNavigationRead(params({ resource: "section", section: "live", offset: 2 ** 32, limit: 7 })),
		).toThrow(/offset/i);
	});

	it("rejects a limit past what a safe integer can carry", () => {
		expect(() =>
			fleet.answerNavigationRead(
				params({ resource: "section", section: "live", offset: 0, limit: Number.MAX_SAFE_INTEGER + 2 }),
			),
		).toThrow(/limit/i);
	});
});

describe("demo fleet truncation", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("keeps large activity trees separate from navigation truncation", () => {
		// Activity size does not truncate a flat navigation page.
		const current = read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "current" }));
		expect(current.truncated).toBe(false);
		const live = read(fleet, params({ resource: "section", section: "live" }));
		expect(live.truncated).toBe(false);
	});

	it("keeps the needs-you page untruncated", () => {
		const needsYou = read(fleet, params({ resource: "section", section: "needs_you" }));
		expect(needsYou.truncated).toBe(false);
	});
});

describe("demo fleet empty option", () => {
	const fleet = createDemoFleet({ now: STARTUP, empty: true });

	it("reports zero live, needs-you, working and errored counts, with sources unaffected", () => {
		const manifest = read(fleet, params({ resource: "manifest" }));
		expect(manifest.sections).toMatchObject({
			live: { count: 0 },
			needs_you: { count: 0 },
			pin_sections: { count: 2 },
		});
		expect(manifest.catalogs).toMatchObject({
			projects: { count: 0 },
			archived_projects: { count: 0 },
			test_runs: { count: 0 },
		});
		expect(manifest.attentionSummary).toEqual({ needsYou: 0, error: 0, working: 0 });
		// The header still names a host: sources are untouched by emptiness.
		expect(manifest.sources).toEqual([
			{ id: "local", label: "this host", kind: "local", online: true },
			{ id: "paradise-park", label: "paradise-park", kind: "appwire", online: true },
		]);
	});

	it("has no live or needs-you rows", () => {
		expect(liveRows(fleet)).toHaveLength(0);
		const needsYou = sessionsOf(read(fleet, params({ resource: "section", section: "needs_you" })));
		expect(needsYou).toHaveLength(0);
	});

	it("keeps both durable pin sections, with no rows pinned in them", () => {
		const catalog = read(fleet, params({ resource: "pin_catalog", limit: 100 }));
		expect(catalog.pin_sections).toEqual([
			{ id: "release", name: "Release", count: 0 },
			{ id: "research", name: "Research", count: 0 },
		]);
	});

	it("has no projects, archived projects or test-run projects in the catalogs", () => {
		const projects = read(fleet, params({ resource: "catalog", catalog: "projects", limit: 100 }));
		expect(projects.projects).toEqual([]);
		const archived = read(fleet, params({ resource: "catalog", catalog: "archived_projects", limit: 100 }));
		expect(archived.projects).toEqual([]);
		const testRuns = read(fleet, params({ resource: "catalog", catalog: "test_runs", limit: 100 }));
		expect(testRuns.projects).toEqual([]);
	});

	it("finds nothing in search, with or without a query", () => {
		const withQuery = fleet.answerSearch({ query: "wasm" });
		expect(withQuery.live).toEqual([]);
		expect(withQuery.past).toEqual([]);
		const noQuery = fleet.answerSearch({});
		expect(noQuery.live).toEqual([]);
		expect(noQuery.past).toEqual([]);
	});
});

describe("demo fleet question after a delay", () => {
	const askedAt = STARTUP + 30 * 1000;
	const fleet = () => createDemoFleet({ now: STARTUP, clock: () => askedAt });
	const needsYouRows = (demo: ReturnType<typeof createDemoFleet>) =>
		sessionsOf(read(demo, params({ resource: "section", section: "needs_you" })));

	it("keeps s-gateway working until the question is asked", () => {
		const demo = fleet();
		const gateway = findRow(liveRows(demo), "s-gateway");
		expect(gateway.state).toBe("active");
		expect(gateway.ask_pending).toBeUndefined();
		expect(needsYouRows(demo).map((row) => row.session_id)).not.toContain(demoSessionId("s-gateway"));
		const manifest = read(demo, params({ resource: "manifest" }));
		expect(manifest.attentionSummary).toEqual({ needsYou: 4, error: 1, working: 9 });
		expect(manifest.sections).toMatchObject({ live: { count: 20 }, needs_you: { count: 4 } });
	});

	it("moves s-gateway into Needs you, shaped like the fleet's own question row, once asked", () => {
		const demo = fleet();
		demo.step("question");
		const gateway = findRow(liveRows(demo), "s-gateway");
		const audit = findRow(liveRows(demo), "s-audit");
		expect(gateway.state).toBe(audit.state);
		expect(gateway.ask_pending).toBe(true);
		expect(gateway.updated_at).toBe(new Date(askedAt).toISOString());
		expect(findRow(needsYouRows(demo), "s-gateway")).toEqual(gateway);
		const manifest = read(demo, params({ resource: "manifest" }));
		expect(manifest.attentionSummary).toEqual({ needsYou: 5, error: 1, working: 8 });
		expect(manifest.sections).toMatchObject({ live: { count: 20 }, needs_you: { count: 5 } });
		const hit = demo.answerSearch({ query: "gateway" }).live[0];
		expect(hit).toMatchObject({ id: demoSessionId("s-gateway"), state: "awaiting", askPending: true });
	});

	it("answers at a higher revision after the question, so an invalidated read is not below target", () => {
		const demo = fleet();
		const before = demo.answerNavigationRead(params({ resource: "section", section: "needs_you" }));
		const payload = demo.step("question");
		const after = demo.answerNavigationRead(params({ resource: "section", section: "needs_you" }));
		expect(after.revision).toBeGreaterThan(before.revision);
		expect(after.etag).not.toBe(before.etag);
		expect(payload).toEqual({
			generationId: DEMO_FLEET_GENERATION,
			sequence: 1,
			targets: [
				{ kind: "manifest", revision: after.revision },
				{ kind: "section", section: "live", revision: after.revision },
				{ kind: "section", section: "needs_you", revision: after.revision },
				{ kind: "project", projectKey: "evener", revision: after.revision },
			],
		});
	});

	it("advertises the question's sequence in its capability, so a client reconnecting afterwards never sees it move backward", () => {
		const demo = fleet();
		expect(demo.navigationCapability()).toEqual({
			version: 1,
			generationId: DEMO_FLEET_GENERATION,
			sequence: 0,
			readVersions: [3],
		});
		const payload = demo.step("question");
		expect(demo.navigationCapability().sequence).toBe(payload.sequence);
	});
});

describe("demo fleet steps for the alert screenshots (phase 6 Task 17)", () => {
	const steppedAt = STARTUP + 30 * 1000;
	const fleet = () => createDemoFleet({ now: STARTUP, clock: () => steppedAt });
	const needsYouRow = (demo: ReturnType<typeof fleet>, slug: string) =>
		sessionsOf(read(demo, params({ resource: "section", section: "needs_you" }))).find(
			(row) => row.session_id === demoSessionId(slug),
		);

	it.each([
		["failure", "s-readintent", { state: "errored" }],
		["approval", "s-landing", { state: "active", approval_pending: true }],
	] as const)("%s moves %s into Needs you, raising the revision the targets name", (step, slug, shape) => {
		const demo = fleet();
		const before = demo.answerNavigationRead(params({ resource: "section", section: "needs_you" }));
		const payload = demo.step(step);
		const after = demo.answerNavigationRead(params({ resource: "section", section: "needs_you" }));
		expect(after.revision).toBeGreaterThan(before.revision);
		expect(payload.sequence).toBe(1);
		expect(payload.targets).toEqual(
			expect.arrayContaining([
				{ kind: "manifest", revision: after.revision },
				{ kind: "section", section: "live", revision: after.revision },
				{ kind: "section", section: "needs_you", revision: after.revision },
			]),
		);
		expect(needsYouRow(demo, slug)).toMatchObject(shape);
		expect(findRow(liveRows(demo), slug)).toMatchObject(shape);
	});

	it("changes nothing, and never throws, when a step's session isn't in the fleet", () => {
		const empty = createDemoFleet({ now: STARTUP, empty: true });
		const payload = empty.step("question");
		// No row changed, so the step bumps neither the sequence nor a target.
		expect(payload).toEqual({ generationId: DEMO_FLEET_GENERATION, sequence: 0, targets: [] });
		expect(liveRows(empty)).toEqual([]);
	});

	it("finish leaves s-resume idle with no question, outside Needs you", () => {
		const demo = fleet();
		demo.step("finish");
		const row = findRow(liveRows(demo), "s-resume");
		expect(row.state).toBe("idle");
		expect(row.ask_pending).toBeUndefined();
		expect(needsYouRow(demo, "s-resume")).toBeUndefined();
	});

	it("takes paradise-park and its rows offline, and brings them back", () => {
		const demo = fleet();
		const payload = demo.step("host-offline");
		expect(payload.targets).toEqual(expect.arrayContaining([expect.objectContaining({ kind: "manifest" })]));
		const sources = () =>
			(read(demo, params({ resource: "manifest" })).sources as { id: string; online: boolean }[]).find(
				(source) => source.id === "paradise-park",
			);
		expect(sources()?.online).toBe(false);
		expect(findRow(liveRows(demo), "s-retry").offline).toBe(true);
		demo.step("host-online");
		expect(sources()?.online).toBe(true);
		expect(findRow(liveRows(demo), "s-retry").offline).toBeUndefined();
		expect(demo.navigationCapability().sequence).toBe(2);
	});
});

describe("demo fleet offline propagation", () => {
	it("marks an offline-host root offline without shipping descendants", () => {
		const fleet = createDemoFleet({ now: STARTUP, offlineHost: true });
		const retry = findRow(liveRows(fleet), "s-retry"); // paradise-park
		expect(retry.offline).toBe(true);
		expect(retry.children).toEqual([]);
	});

	it("leaves a local root online under another host's offline flag", () => {
		const fleet = createDemoFleet({ now: STARTUP, offlineHost: true });
		const pr2138 = findRow(liveRows(fleet), "s-pr2138"); // local/magic-kingdom
		expect(pr2138.offline).toBeUndefined();
		expect(pr2138.children).toEqual([]);
	});
});

describe("demo fleet archive", () => {
	const fleet = () => createDemoFleet({ now: STARTUP });
	const tierOf = (demo: ReturnType<typeof createDemoFleet>, ref: string) =>
		read(demo, params({ resource: "location", ref })).tier;
	// Every resource the archive changes, at the fleet's one new revision,
	// and the loaded project pages, as the real hub's session archive names
	// them (cmd/evener-hub/app_archive.go, navigation_service.go's
	// commitTargetsLocked).
	const archiveTargets = (revision: number, projectKey: string) => [
		{ kind: "manifest", revision },
		{ kind: "section", section: "live", revision },
		{ kind: "section", section: "needs_you", revision },
		{ kind: "catalog", catalog: "projects", revision },
		{ kind: "catalog", catalog: "archived_projects", revision },
		{ kind: "project", projectKey, revision },
		{ kind: "all_loaded_projects" },
	];

	it("offers Archive on every Live row: a local row by its session id, another host's by its ref", () => {
		for (const row of liveRows(fleet())) {
			expect(archiveTarget(row)).toEqual({ kind: "session", id: row.host_id === "local" ? row.session_id : row.ref });
		}
	});

	it("takes an archived session out of Live and answers with the receipt and invalidation a real hub sends", () => {
		const demo = fleet();
		const deslop = findRow(liveRows(demo), "s-deslop");
		const before = demo.answerNavigationRead(params({ resource: "section", section: "live" }));
		const { response, invalidated } = demo.archive({ kind: "session", id: deslop.session_id, archived: true });
		const after = demo.answerNavigationRead(params({ resource: "section", section: "live" }));
		expect(after.revision).toBeGreaterThan(before.revision);
		expect(liveRows(demo).map((row) => row.session_id)).not.toContain(deslop.session_id);
		expect(liveRows(demo)).toHaveLength(19);
		expect(tierOf(demo, deslop.ref)).toBe("archived");
		const targets = archiveTargets(after.revision, "deslop");
		expect(response).toEqual({ ok: true, navigation: { generation_id: DEMO_FLEET_GENERATION, targets } });
		expect(invalidated).toEqual({ generationId: DEMO_FLEET_GENERATION, sequence: 1, targets });
		expect(demo.navigationCapability().sequence).toBe(1);
	});

	it("brings an unarchived session back to Live as it was, where it was", () => {
		const demo = fleet();
		const order = liveRows(demo).map((row) => row.session_id);
		const deslop = findRow(liveRows(demo), "s-deslop");
		demo.archive({ kind: "session", id: deslop.session_id, archived: true });
		const { invalidated } = demo.archive({ kind: "session", id: deslop.session_id, archived: false });
		expect(findRow(liveRows(demo), "s-deslop")).toEqual(deslop);
		expect(liveRows(demo).map((row) => row.session_id)).toEqual(order);
		expect(tierOf(demo, deslop.ref)).toBe("current");
		expect(invalidated.sequence).toBe(2);
	});

	it("archives another host's session by its ref, and a local one by its local: ref", () => {
		const demo = fleet();
		const retry = findRow(liveRows(demo), "s-retry");
		const deslop = findRow(liveRows(demo), "s-deslop");
		demo.archive({ kind: "session", id: retry.ref, archived: true });
		demo.archive({ kind: "session", id: deslop.ref, archived: true });
		expect(tierOf(demo, retry.ref)).toBe("archived");
		expect(tierOf(demo, deslop.ref)).toBe("archived");
	});

	it("keeps an archive when the working row later asks its question", () => {
		const demo = fleet();
		const deslop = findRow(liveRows(demo), "s-deslop");
		demo.archive({ kind: "session", id: deslop.session_id, archived: true });
		demo.step("question");
		expect(liveRows(demo).map((row) => row.session_id)).not.toContain(deslop.session_id);
		expect(findRow(liveRows(demo), "s-gateway").ask_pending).toBe(true);
	});

	it("keeps an archived session in its own project's archived list, the tier its location names", () => {
		const demo = fleet();
		const deslop = findRow(liveRows(demo), "s-deslop");
		demo.archive({ kind: "session", id: deslop.session_id, archived: true });
		const location = read(demo, params({ resource: "location", ref: deslop.ref }));
		expect(location).toMatchObject({ project_key: "deslop", tier: "archived" });
		// Its only session archived, deslop moves to Archived projects, which
		// the list reads when asked, as the location screen asks, with no
		// catalog.
		const list = demo.answerArchivedList({ projectKey: "deslop" });
		expect(list.catalog).toBe("archived_projects");
		expect(decodeArchivedListSessions(list.sessions).map((row) => row.session_id)).toEqual([deslop.session_id]);
	});

	it("moves a project whose every session is archived into Archived projects, and back on unarchive", () => {
		const demo = fleet();
		const keys = (catalog: string) =>
			(read(demo, params({ resource: "catalog", catalog, limit: 100 })).projects as { key: string }[]).map(
				(project) => project.key,
			);
		const projectsBefore = keys("projects");
		demo.archive({ kind: "session", id: demoSessionId("s-deslop"), archived: true });
		expect(keys("projects")).not.toContain("deslop");
		expect(read(demo, params({ resource: "catalog", catalog: "archived_projects", limit: 100 })).projects).toEqual([
			expect.objectContaining({ key: "evener", session_count: 271 }),
			expect.objectContaining({ key: "deslop", session_count: 1 }),
		]);
		expect(read(demo, params({ resource: "manifest" })).catalogs).toMatchObject({
			projects: { count: projectsBefore.length - 1 },
			archived_projects: { count: 2 },
		});
		demo.archive({ kind: "session", id: demoSessionId("s-deslop"), archived: false });
		expect(keys("projects")).toEqual(projectsBefore);
		expect(keys("archived_projects")).toEqual(["evener"]);
	});

	it("keeps an archived pinned session in its pin section", () => {
		const demo = fleet();
		const pinCatalog = () => read(demo, params({ resource: "pin_catalog", limit: 100 })).pin_sections;
		const research = () =>
			sessionsOf(read(demo, params({ resource: "pin_section", sectionId: "research" }))).map((row) => row.session_id);
		const catalogBefore = pinCatalog();
		const researchBefore = research();
		const pinSectionsBefore = (read(demo, params({ resource: "manifest" })).sections as { pin_sections: unknown })
			.pin_sections;
		demo.archive({ kind: "session", id: demoSessionId("s-diff"), archived: true });
		expect(research()).toContain(demoSessionId("s-diff"));
		expect(research()).toEqual(researchBefore);
		expect(pinCatalog()).toEqual(catalogBefore);
		expect((read(demo, params({ resource: "manifest" })).sections as { pin_sections: unknown }).pin_sections).toEqual(
			pinSectionsBefore,
		);
	});

	it("stops counting an archived working or failed row in the manifest's summary", () => {
		const demo = fleet();
		demo.archive({ kind: "session", id: demoSessionId("s-gateway"), archived: true });
		demo.archive({ kind: "session", id: findRow(liveRows(demo), "s-retry").ref, archived: true });
		const manifest = read(demo, params({ resource: "manifest" }));
		expect(manifest.attentionSummary).toEqual({ needsYou: 3, error: 0, working: 8 });
		expect(manifest.sections).toMatchObject({ live: { count: 18 }, needs_you: { count: 3 } });
	});

	it("refuses a project archive and a session it doesn't hold, changing nothing", () => {
		const demo = fleet();
		expect(() => demo.archive({ kind: "project", id: "deslop", archived: true })).toThrow(
			"The demo fleet archives sessions only",
		);
		expect(() => demo.archive({ kind: "session", id: "0000000000000000000000", archived: true })).toThrow(
			"Unknown demonstration session: 0000000000000000000000",
		);
		expect(demo.navigationCapability().sequence).toBe(0);
	});
});

// The Activity list, a subagent's screen and the Reader read the same swarm
// the Board's rows come from (spec Appendix B), through the typed activity
// reads.
describe("demo fleet subagents", () => {
	const fleet = createDemoFleet({ now: STARTUP });
	const pr2138 = `local:${demoSessionId("s-pr2138")}`;
	const subtree = (ref: string) => fleet.answerDelegatesList({ ref, scope: "subtree", limit: 200 }).delegates;

	it("counts Get PR 2138 Test Clean's 55 subagents and lists them with data.js's details", () => {
		expect(fleet.answerActivityRead({ ref: pr2138, scope: "subtree" }).delegates).toEqual({
			known: true,
			total: 55,
			active: 32,
			failed: 2,
			completed: 21,
		});
		const rows = subtree(pr2138);
		expect(rows).toHaveLength(55);
		expect(rows.find((row) => row.description === "Fix race in tree settle")).toMatchObject({
			outcome: "failed",
			worktree: { branch: "fix-settle-race" },
			usage: { totalTokens: 1_200_000 },
		});
	});

	it("gives a subagent that started subagents its own, as its transcript's delegates name them", () => {
		const settle = subtree(pr2138).find((row) => row.description === "Fix race in tree settle");
		if (!settle) throw new Error("no Fix race in tree settle");
		const own = fleet.answerDelegatesList({ ref: settle.childRef });
		expect(own.context.ref).toBe(settle.childRef);
		expect(own.delegates.map((row) => row.description)).toEqual(["Check drain ordering in tests"]);
	});

	it("gives a session with no subagents or shell jobs complete, empty lists", () => {
		const ref = `local:${demoSessionId("s-gateway")}`;
		const delegates = fleet.answerDelegatesList({ ref });
		expect(delegates.delegates).toEqual([]);
		expect(delegates.page).toEqual({ complete: true, issues: [] });
		const jobs = fleet.answerSessionJobsList({ ref });
		expect(jobs.jobs).toEqual([]);
		expect(jobs.page).toEqual({ complete: true, issues: [] });
	});
});

// The real hub serves a project's archived rows only through
// evener/archived/list (cmd/evener-hub/navigation_archived_list.go): paged by
// an opaque cursor bound to its catalog hint and project, with the tier's
// total; navigation's archived project_page and the project overview's
// archived tier come back empty.
describe("demo fleet archived lists", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	/** The session ids one archived list serves, paging by cursor with the
	 * same params, each page checked to hold rows and the list's total. */
	function pageArchived(list: Omit<ArchivedListParams, "cursor">, total: number): Set<string> {
		const seen = new Set<string>();
		let cursor: string | undefined;
		for (let guard = 0; ; guard++) {
			if (guard > 20) throw new Error("archived paging never reached the end");
			const page = fleet.answerArchivedList({ ...list, cursor });
			expect(page.total).toBe(total);
			const rows = decodeArchivedListSessions(page.sessions);
			expect(rows.length).toBeGreaterThan(0);
			for (const row of rows) seen.add(row.session_id);
			cursor = page.nextCursor;
			if (!cursor) return seen;
		}
	}

	it("pages a project's archived sessions by cursor to the end, with the tier's total", () => {
		expect(pageArchived({ catalog: "archived_projects", projectKey: "evener" }, 271).size).toBe(271);
	});

	it("takes a limit up to the section maximum, where none or 0 means the maximum", () => {
		const list = { catalog: "archived_projects", projectKey: "evener" };
		expect(fleet.answerArchivedList(list).sessions).toHaveLength(50);
		expect(fleet.answerArchivedList({ ...list, limit: 0 }).sessions).toHaveLength(50);
		const ten = fleet.answerArchivedList({ ...list, limit: 10 });
		expect(ten.sessions).toHaveLength(10);
		const after = fleet.answerArchivedList({ ...list, limit: 10, cursor: ten.nextCursor });
		expect(decodeArchivedListSessions(after.sessions)[0]?.ref).toBe(
			decodeArchivedListSessions(fleet.answerArchivedList({ ...list, limit: 11 }).sessions)[10]?.ref,
		);
		for (const limit of [-1, 51])
			expect(() => fleet.answerArchivedList({ ...list, limit })).toThrow("limit must be between 0 and 50");
	});

	it("serves navigation's archived project_page and overview tier empty, as the hub does", () => {
		const page = read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "archived" }));
		expect(sessionsOf(page)).toEqual([]);
		expect(page.remaining).toBe(0);
		const overview = read(fleet, params({ resource: "project", projectKey: "evener" }));
		expect((overview.archived as { sessions: unknown[] }).sessions).toEqual([]);
	});

	it("answers a project its catalog doesn't hold with an empty list", () => {
		expect(fleet.answerArchivedList({ catalog: "test_runs", projectKey: "evener" })).toEqual({
			sessions: [],
			total: 0,
		});
	});

	// The catalog is a hint, as on the hub: the hinted catalog when it holds the
	// project, the other of projects and archived projects when the project
	// moved there, and with no hint the first catalog holding it. The answer
	// says which catalog it read.
	it("reads the hinted catalog when it holds the project, and says which", () => {
		for (const catalog of ["projects", "archived_projects"])
			expect(fleet.answerArchivedList({ catalog, projectKey: "evener", limit: 1 })).toMatchObject({
				catalog,
				total: 271,
			});
	});

	it("reads the first catalog holding the project when given no hint", () => {
		expect(fleet.answerArchivedList({ projectKey: "evener", limit: 1 })).toMatchObject({
			catalog: "projects",
			total: 271,
		});
		expect(fleet.answerArchivedList({ projectKey: "hub-test-env" })).toEqual({
			sessions: [],
			total: 0,
			catalog: "test_runs",
		});
	});

	it("follows a project to the other member of the pair, and never into or out of test runs", () => {
		expect(fleet.answerArchivedList({ catalog: "archived_projects", projectKey: "deslop" })).toEqual({
			sessions: [],
			total: 0,
			catalog: "projects",
		});
		expect(fleet.answerArchivedList({ catalog: "projects", projectKey: "hub-test-env" })).toEqual({
			sessions: [],
			total: 0,
		});
	});

	// A project whose every session is archived moves into Archived projects,
	// while a row read earlier still names Projects.
	it("follows a project that moved into Archived projects, whichever of the pair it is hinted", () => {
		const demo = createDemoFleet({ now: STARTUP });
		const deslop = findRow(liveRows(demo), "s-deslop");
		demo.archive({ kind: "session", id: deslop.session_id, archived: true });
		for (const catalog of [undefined, "", "projects", "archived_projects"])
			expect(demo.answerArchivedList({ catalog, projectKey: "deslop" })).toMatchObject({
				catalog: "archived_projects",
				total: 1,
			});
	});

	it("binds a cursor to the hint it was read with", () => {
		expect(pageArchived({ projectKey: "evener" }, 271).size).toBe(271);
		const unhinted = fleet.answerArchivedList({ projectKey: "evener" }).nextCursor;
		expect(() => fleet.answerArchivedList({ catalog: "projects", projectKey: "evener", cursor: unhinted })).toThrow(
			"cursor belongs to another archived list",
		);
		const hinted = fleet.answerArchivedList({ catalog: "projects", projectKey: "evener" }).nextCursor;
		expect(() => fleet.answerArchivedList({ projectKey: "evener", cursor: hinted })).toThrow(
			"cursor belongs to another archived list",
		);
	});

	it("refuses an unknown catalog and a cursor from another list", () => {
		expect(() => fleet.answerArchivedList({ catalog: "nope", projectKey: "evener" })).toThrow(
			"Unknown demonstration catalog: nope",
		);
		// The catalog is checked before the limit, as on the hub.
		expect(() => fleet.answerArchivedList({ catalog: "nope", projectKey: "evener", limit: -1 })).toThrow(
			"Unknown demonstration catalog: nope",
		);
		const cursor = fleet.answerArchivedList({ catalog: "archived_projects", projectKey: "evener" }).nextCursor;
		expect(() => fleet.answerArchivedList({ catalog: "projects", projectKey: "evener", cursor })).toThrow(
			"cursor belongs to another archived list",
		);
		expect(() => fleet.answerArchivedList({ catalog: "archived_projects", projectKey: "deslop", cursor })).toThrow(
			"cursor belongs to another archived list",
		);
		expect(() => fleet.answerArchivedList({ catalog: "projects", projectKey: "evener", cursor: "junk" })).toThrow(
			"invalid cursor",
		);
	});
});

// S5's pulse read (evener/activity/read): the Board's working-row meters, its
// subagent tally, the Quiet and May-be-stuck labels and the latest tool intent.
// This is a different method from evener/thread/activity/read above.
describe("demo fleet pulse activity (evener/activity/read)", () => {
	const fleet = createDemoFleet({ now: STARTUP, clock: () => STARTUP });
	const read = (params: { refs?: string[] } = {}) => decodeActivityRead(fleet.answerPulseRead(params));
	// fleetSessionRef resolves a slug's real host-prefixed ref, so a remote slug
	// like s-wasm (paradise-park) can't silently resolve to undefined.
	const refOf = fleetSessionRef;
	const byRef = (sessions: ReturnType<typeof read>) => new Map(sessions.map((session) => [session.ref, session]));

	it("answers a decoder-valid entry for every live top-level session, and none for an ended or archived one", () => {
		const sessions = read();
		// A malformed entry is dropped by the decoder, so a full count proves
		// every shape is one the wire accepts.
		expect(sessions).toHaveLength(20);
		const refs = new Set(sessions.map((session) => session.ref));
		expect(refs.has(refOf("s-roster"))).toBe(false); // shut down
		expect(refs.has(refOf("s-fuzz"))).toBe(false); // archived
		for (const session of sessions) {
			expect(session.minutes).toHaveLength(7);
			expect(session.minutes.every((events) => Number.isSafeInteger(events) && events >= 0)).toBe(true);
			expect(Number.isSafeInteger(session.runningSubagents) && session.runningSubagents >= 0).toBe(true);
		}
	});

	it("honors refs: only named live sessions return, and an unknown ref is simply absent", () => {
		const ref = refOf("s-pr2138");
		expect(read({ refs: [ref] }).map((session) => session.ref)).toEqual([ref]);
		expect(read({ refs: ["local:nope"] })).toEqual([]);
	});

	it("counts a session waiting on subagents and withholds its quiet time", () => {
		const pr2138 = byRef(read()).get(refOf("s-pr2138"));
		expect(pr2138).toMatchObject({ runningSubagents: 32 });
		expect(pr2138).not.toHaveProperty("quietForMs");
		// "Waiting on 31 subagents" is a wait, not a tool intent.
		expect(pr2138).not.toHaveProperty("latestIntent");
	});

	it("carries a working session's latest tool intent, never its activity label", () => {
		const bySession = byRef(read());
		expect(bySession.get(refOf("s-resume"))?.latestIntent).toBe("Reading agent/session_resume.go");
		expect(bySession.get(refOf("s-stumble"))?.latestIntent).toBe("Editing agent/tool_repair.go");
		// "Thinking" and "Running <cmd>" name no tool call: the row falls back to
		// its running job, or to "Working".
		expect(bySession.get(refOf("s-gateway"))?.latestIntent).toBeUndefined();
		expect(bySession.get(refOf("s-tasklist"))?.latestIntent).toBeUndefined();
	});

	it("shows the Board's Quiet and May-be-stuck states on silently working sessions", () => {
		const bySession = byRef(read());
		expect(quietState(bySession.get(refOf("s-landing"))!, 0)).toEqual({ state: "quiet", forMs: 4 * 60_000 });
		expect(quietState(bySession.get(refOf("s-gateway"))!, 0)).toEqual({ state: "stuck", forMs: 12 * 60_000 });
	});

	it("grows a working session's quiet time with the injected clock", () => {
		const later = createDemoFleet({ now: STARTUP, clock: () => STARTUP + 90_000 });
		const landing = later.answerPulseRead({}).sessions.find((session) => session.ref === refOf("s-landing"));
		expect(landing?.quietForMs).toBe(4 * 60_000 + 90_000);
	});

	it("withholds quiet time from every session that is not silently working", () => {
		const bySession = byRef(read());
		expect(bySession.get(refOf("s-diff"))?.quietForMs).toBeUndefined(); // idle
		// s-wasm runs on paradise-park: assert the entry is really there, or the
		// lookup would pass whether or not quiet time is emitted.
		expect(bySession.has(refOf("s-wasm"))).toBe(true);
		expect(bySession.get(refOf("s-wasm"))?.quietForMs).toBeUndefined(); // working, two subagents run
		for (const session of bySession.values()) expect(session.quietForMs ?? 0).toBeGreaterThanOrEqual(0);
	});

	it("draws a pulse, and flattens the minutes a silent session's quiet gap covers", () => {
		const bySession = byRef(read());
		expect(bySession.get(refOf("s-diff"))?.minutes).toEqual([0, 0, 0, 0, 0, 0, 0]); // idle
		expect(bySession.get(refOf("s-tasklist"))?.minutes).toEqual([4, 8, 4, 12, 8, 16, 8]); // busy
		// s-landing has been quiet 4m: its newest four minutes drew nothing.
		expect(bySession.get(refOf("s-landing"))?.minutes).toEqual([1, 2, 1, 0, 0, 0, 0]);
		// s-gateway has been quiet 12m: its whole window is flat.
		expect(bySession.get(refOf("s-gateway"))?.minutes).toEqual([0, 0, 0, 0, 0, 0, 0]);
	});

	it("keeps a session whose quiet time is recomputed after a state change", () => {
		// A row that changes state after startup gets a fractional, generally
		// negative `ago` (commitRowState). The quiet time built from it must stay
		// a whole, non-negative millisecond value, or the decoder drops the whole
		// entry and the Board loses that session's meter and labels.
		const later = createDemoFleet({ now: STARTUP, clock: () => STARTUP + 1001 });
		later.setSessionState(refOf("s-diff"), "working");
		const sessions = decodeActivityRead(later.answerPulseRead({ refs: [refOf("s-diff")] }));
		expect(sessions.map((session) => session.ref)).toEqual([refOf("s-diff")]);
		expect(sessions[0]?.quietForMs).toBe(0);
	});
});
