// The redesign's demo fleet, decoded through the real package codec so the
// fixture can never drift from the wire (see demoFleet.mts's header comment
// for what this fixture represents and where it comes from).
import { describe, expect, it } from "vitest";
import type { NavigationReadParams, NavigationSessionSummary } from "@evener/appwire-client";
import {
	decodeNavigationResponse,
	materializeSnapshot,
	navigationParamsToResourceKey,
} from "@evener/appwire-client/state/navigation";
import { createDemoFleet } from "./demoFleet.mjs";

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
	return { representationVersion: 2, offset: 0, limit: 50, ...overrides };
}

function sessionsOf(materialized: Record<string, unknown>): NavigationSessionSummary[] {
	return materialized.sessions as NavigationSessionSummary[];
}

function findRow(rows: NavigationSessionSummary[], sessionId: string): NavigationSessionSummary {
	const row = rows.find((row) => row.session_id === sessionId);
	if (!row) throw new Error(`missing session ${sessionId} in [${rows.map((r) => r.session_id).join(", ")}]`);
	return row;
}

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

describe("demo fleet live and needs-you sections", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("holds the 20 live top-level sessions Appendix B's fixture actually contains", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		expect(rows).toHaveLength(20);
		expect(new Set(rows.map((row) => row.session_id)).size).toBe(20);
	});

	it("maps the prototype's states the way the brief spells out: failed, question, approval and so on", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		expect(findRow(rows, "s-retry")).toMatchObject({ state: "errored", live: true, host_id: "paradise-park" });
		expect(findRow(rows, "s-audit")).toMatchObject({ state: "awaiting", ask_pending: true });
		expect(findRow(rows, "s-mirror")).toMatchObject({ state: "active" });
		expect(findRow(rows, "s-namer")).toMatchObject({ state: "restartRequired" });
		expect(findRow(rows, "s-hier")).toMatchObject({ state: "awaiting" }); // "your move": turn ended, ask_pending false
		expect(findRow(rows, "s-diff")).toMatchObject({ state: "idle" });
		expect(findRow(rows, "s-pr2138")).toMatchObject({ state: "active" });
	});

	it("gives local sessions a local: ref and remote ones a host-prefixed ref", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		expect(findRow(rows, "s-audit").ref).toBe("local:s-audit");
		expect(findRow(rows, "s-retry").ref).toBe("paradise-park:s-retry");
		expect(findRow(rows, "s-retry").host_id).toBe("paradise-park");
	});

	it("computes updated_at relative to startup", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		// s-retry: ago 2 minutes.
		expect(findRow(rows, "s-retry").updated_at).toBe(new Date(STARTUP - 2 * 60 * 1000).toISOString());
	});

	it("holds exactly the 4 needs-you rows: the failure, the question, the approval and the restart", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "needs_you" })));
		expect(rows.map((row) => row.session_id).sort()).toEqual(["s-audit", "s-mirror", "s-namer", "s-retry"].sort());
		// Approval rows keep state "active"; the phone infers approval from
		// showing up here, per the task's own background note.
		expect(findRow(rows, "s-mirror").state).toBe("active");
	});

	it("marks every paradise-park row offline under EVENER_DEMO_FLEET_OFFLINE_HOST, never local rows", () => {
		const offlineFleet = createDemoFleet({ now: STARTUP, offlineHost: true });
		const rows = sessionsOf(read(offlineFleet, params({ resource: "section", section: "live" })));
		expect(findRow(rows, "s-retry").offline).toBe(true); // paradise-park
		expect(findRow(rows, "s-audit").offline).toBeUndefined(); // local
		const defaultRows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		expect(findRow(defaultRows, "s-retry").offline).toBeUndefined();
	});
});

describe("demo fleet subagent trees", () => {
	const fleet = createDemoFleet({ now: STARTUP });

	it("caps a big swarm at the hub's own child limit and counts the rest as omitted", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		const pr2138 = findRow(rows, "s-pr2138");
		// 54 real subagents (Appendix B: "subagent trees from 0 to 54"), capped at
		// the hub's maxNavigationChildren (cmd/evener-hub/navigation_projection.go).
		expect(pr2138.children).toHaveLength(50);
		expect(pr2138.omitted_descendants).toBe(4);
		expect(pr2138.children.every((child) => child.kind === "subagent")).toBe(true);
		const settle = pr2138.children.find((child) => child.session_id === "g-settle");
		expect(settle).toMatchObject({ state: "errored", live: false });
		expect(settle?.children).toHaveLength(1);
		expect(settle?.children[0]).toMatchObject({ session_id: "g-settle-1", state: "active", live: true });
	});

	it("gives a small named swarm its real titles (s-retry: r-1..r-4)", () => {
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		const retry = findRow(rows, "s-retry");
		expect(retry.children.map((child) => child.session_id)).toEqual(["r-1", "r-2", "r-3", "r-4"]);
		expect(retry.children.map((child) => child.state)).toEqual(["ended", "ended", "errored", "ended"]);
		// Children run on the same host as their parent.
		expect(retry.children.every((child) => child.host_id === "paradise-park")).toBe(true);
	});

	it("carries the one-of-467-in-Archived case (s-fuzz) with the same cap and a large omitted count", () => {
		const archived = sessionsOf(
			read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "archived" })),
		);
		const fuzz = findRow(archived, "s-fuzz");
		expect(fuzz.children).toHaveLength(50);
		expect(fuzz.omitted_descendants).toBe(467 - 50);
	});
});

describe("demo fleet running jobs", () => {
	it("surfaces a working session's shell command as a running job", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const rows = sessionsOf(read(fleet, params({ resource: "section", section: "live" })));
		const tasklist = findRow(rows, "s-tasklist");
		expect(tasklist.running_jobs).toEqual([
			expect.objectContaining({ status: "running", command: "go test ./cmd/evener-hub/..." }),
		]);
		// "Thinking" and similar activity lines are not commands.
		expect(findRow(rows, "s-gateway").running_jobs).toBeUndefined();
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
		expect(rows.map((row) => row.session_id).sort()).toEqual(["s-jobdisp", "s-pr2138"]);
	});

	it("rejects an unknown pin section instead of returning an empty page silently", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "pin_section", sectionId: "nope" }))).toThrow();
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

	it("puts the 271-session archived total on evener's archived_projects row", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "archived_projects", limit: 100 }));
		expect(catalog.projects).toEqual([expect.objectContaining({ key: "evener", session_count: 271 })]);
	});

	it("groups the three test-run sessions under the synthetic hub-test-env project", () => {
		const catalog = read(fleet, params({ resource: "catalog", catalog: "test_runs", limit: 100 }));
		expect(catalog.projects).toEqual([expect.objectContaining({ key: "hub-test-env", session_count: 3 })]);
		const page = sessionsOf(read(fleet, params({ resource: "project_page", projectKey: "hub-test-env", tier: "current" })));
		expect(page.map((row) => row.session_id).sort()).toEqual(["s-test1", "s-test2", "s-test3"]);
	});

	it("splits a project's sessions into today (current) and older (recent) tiers", () => {
		const current = sessionsOf(read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "current" })));
		const recent = sessionsOf(read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "recent" })));
		expect(current.map((row) => row.session_id)).toContain("s-retry"); // ago 2m: today
		expect(recent.map((row) => row.session_id)).toContain("s-roster"); // ago 1d: recent
		expect(recent.map((row) => row.session_id)).not.toContain("s-retry");
	});

	it("reports the true archived remaining count on the archived tier page, not the zeroed project overview", () => {
		const page = read(fleet, params({ resource: "project_page", projectKey: "evener", tier: "archived" }));
		expect(page.remaining).toBe(271 - 5);
		const overview = read(fleet, params({ resource: "project", projectKey: "evener" }));
		expect((overview.archived as { sessions: unknown[] }).sessions).toHaveLength(5);
	});

	it("rejects an unknown project", () => {
		expect(() => fleet.answerNavigationRead(params({ resource: "project", projectKey: "nope" }))).toThrow();
	});
});

describe("demo fleet search, auth and plugins", () => {
	it("finds live and past sessions by title", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const response = fleet.answerSearch({ query: "wasm" });
		expect(response.live.map((hit) => hit.id)).toEqual(["s-wasm"]);
		expect(response.past.map((hit) => hit.id)).toEqual(["s-wasm2"]);
		expect(response.live[0]).toMatchObject({ project: "c-to-wasm", ref: "paradise-park:s-wasm" });
	});

	it("reports one provider needing sign-in, so the Board's notice can show", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		const response = fleet.answerAuthList();
		expect(response.providers).toEqual([
			expect.objectContaining({ provider: "codex-jesse-fsck.com", needsLogin: true }),
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
});

describe("demo fleet error handling", () => {
	it("fails loudly on a navigation resource kind it doesn't serve", () => {
		const fleet = createDemoFleet({ now: STARTUP });
		expect(() => fleet.answerNavigationRead(params({ resource: "location", ref: "local:s-retry" }))).toThrow();
	});
});
