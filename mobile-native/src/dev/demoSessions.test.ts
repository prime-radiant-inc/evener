// The demo hub's sessions for Appendix A's Session frames, read the way the
// phone reads them: each thread hydrated by the package (hydrateThread) and
// projected at a detail level (projectConversation), then asked the same
// questions the Session's own screen asks. This keeps the fixture honest to
// the wire (see demoSessions.ts's header comment).
import { describe, expect, it } from "vitest";
import { hydrateThread, type Thread } from "@evener/appwire-client";
import type { ContentLevel } from "@evener/appwire-client";
import { liveAsksFor, projectConversation } from "../projectedRows.js";
import { projectNativeTranscript } from "../transcriptPresentation.js";
import { groupTimeline, type TimelineRow } from "../timeline.js";
import { configForLevel } from "../session/detailLevels.js";
import { EVIDENCE_PREVIEW_LINES, stepEvidence } from "../session/evidence.js";
import { ghosts, shownGhosts } from "../session/ghosts.js";
import { modelChipLabel, notesSummary } from "../session/sessionFacts.js";
import { canWriteHumanNote, notesBarPreview } from "../session/sessionNotes.js";
import { contextChips, sessionStateLine } from "../session/sessionState.js";
import { subagentLine } from "../session/subagentLine.js";
import { runSummary, runSummaryText, sessionRows } from "../session/transcriptRows.js";
import { demoSessionId, fleetSessionRef, fleetSessions } from "./demoFleet.js";
import { createDemoSessions, DEMO_MODEL_LIST } from "./demoSessions.js";

const NOW = Date.parse("2026-09-28T21:00:00.000Z");
const sessions = createDemoSessions({ now: NOW });

function threadOf(slug: string): Thread {
	const thread = sessions.find((candidate) => candidate.evener.ref === fleetSessionRef(slug));
	if (!thread) throw new Error(`no demo thread for ${slug}`);
	return thread;
}

const hostOf = (ref: string) => ref.slice(0, ref.indexOf(":"));

// One session as the Session screen sees it at a detail level: the store's
// projection, then the screen's presentation and transcript rows.
function open(slug: string, level: ContentLevel = "intent") {
	const thread = threadOf(slug);
	const model = hydrateThread({ thread }, thread.evener.ref, NOW);
	const config = configForLevel(level, null) ?? undefined;
	const conversation = projectConversation(model, liveAsksFor(model), config);
	const rows = sessionRows(groupTimeline(projectNativeTranscript(conversation, config).items), model.turns);
	return { thread, model, conversation, rows };
}

type Run = Extract<TimelineRow, { kind: "run" }>;
type Activity = Extract<TimelineRow, { kind: "activity" }>;
const runsOf = (rows: TimelineRow[]) => rows.filter((row): row is Run => row.kind === "run");
const subagentsOf = (rows: TimelineRow[]) =>
	rows.filter((row): row is Activity => row.kind === "activity" && row.label === "delegate");

describe("the demo sessions behind Appendix A's Session frames", () => {
	it("serves a valid thread for every fleet session, so the title's swipe always lands on a real neighbor", () => {
		const refs = fleetSessions().map((session) => session.ref);
		expect(sessions.slice(0, refs.length).map((thread) => thread.evener.ref)).toEqual(refs);
		// No two threads, subagents included, share a hub id.
		const ids = sessions.map((thread) => thread.sessionId);
		expect(new Set(ids).size).toBe(ids.length);
		for (const session of fleetSessions()) {
			const { thread, conversation } = open(session.slug);
			expect(thread.name).toBe(session.title);
			expect(conversation.items.length).toBeGreaterThan(0);
			expect(thread.evener.capabilities.sharedNotes).toBe(true);
			// As the Go encoder writes items: your message is completed, and a
			// thought with no text yet leaves the text out (omitempty).
			for (const item of thread.turns?.flatMap((turn) => turn.items ?? []) ?? []) {
				if (item.type === "userMessage") expect(item.status).toBe("completed");
				if (item.type === "reasoning") expect(item).not.toHaveProperty("text");
			}
		}
	});

	it("serves a thread for every subagent a session's delegates name, so opening one reads it", () => {
		const byRef = new Map(sessions.map((thread) => [thread.evener.ref, thread]));
		const delegates = sessions.flatMap((thread) =>
			(thread.evener.diagnostics?.delegates ?? []).map((delegate) => ({ parent: thread, delegate })),
		);
		expect(delegates.length).toBeGreaterThan(55);
		for (const { parent, delegate } of delegates) {
			const thread = byRef.get(delegate.transcriptRef);
			if (!thread) throw new Error(`no thread for ${delegate.transcriptRef}`);
			expect(thread.name).toBe(delegate.description);
			expect(thread.sessionId).toBe(delegate.childSessionId);
			// Its status matches its row: running works, failed failed, done ended.
			expect(thread.status.type).toBe(
				{ running: "active", failed: "systemError", completed: "notLoaded" }[delegate.status],
			);
			expect(thread.evener.parentRef).toBe(
				delegate.parentDelegateId
					? byRef.get(hostOf(parent.evener.ref) + ":" + demoSessionId(delegate.parentDelegateId))?.evener.ref
					: parent.evener.ref,
			);
			const model = hydrateThread({ thread }, thread.evener.ref, NOW);
			expect(projectConversation(model, liveAsksFor(model)).items.length).toBeGreaterThan(0);
		}
	});

	it("names each thread by its session id, the id part of its Board ref, as the hub does", () => {
		// The hub's thread id is its session id (cmd/evener-hub/app_threadread.go);
		// the Subagents list checks a tree's root against it.
		const idOf = (ref: string) => ref.slice(ref.indexOf(":") + 1);
		for (const thread of sessions) {
			expect(thread.id).toBe(idOf(thread.evener.ref));
			expect(thread.sessionId).toBe(thread.id);
		}
		expect(threadOf("s-pr2138").id).toBe(demoSessionId("s-pr2138"));
	});

	it("frame 7: a working session at Intent, with its chips, notes, runs and a failed subagent", () => {
		const { model, rows } = open("s-pr2138");
		expect(model.name).toBe("Get PR 2138 Test Clean");
		expect(sessionStateLine(model, NOW).text).toMatch(/^Working · \d+[smh]/);
		expect(contextChips(model, true).map(({ label, failed }) => ({ label, failed }))).toEqual([
			{ label: "Subagents 55", failed: "2 failed" },
			{ label: "Tasks 3/7", failed: undefined },
			{ label: "Goal", failed: undefined },
			{ label: "Queue 1", failed: undefined },
		]);
		expect(notesBarPreview(model)).toEqual({
			glyph: "person",
			text: "Your note: Don't skip or quarantine tests. Fix causes.",
			links: "3 links",
		});
		expect(notesSummary(model)).toBe("Your note · agent note · 3 links");
		expect(model.sessionUrls.map((link) => link.url)).toEqual([
			"https://github.com/prime-radiant-inc/evener/pull/2138",
			"https://github.com/prime-radiant-inc/evener/pull/2138/checks",
			"file:///home/jesse/git/prime-radiant-inc/evener/docs/superpowers/plans/2026-09-25-settle-race.md",
		]);
		expect(rows.some((row) => row.kind === "user")).toBe(true);
		expect(rows.some((row) => row.kind === "assistant")).toBe(true);
		const runs = runsOf(rows);
		expect(runs.length).toBeGreaterThanOrEqual(2);
		for (const run of runs) expect(runSummaryText(runSummary(run.steps))).toMatch(/^\d+ steps? · \d+[smh]/);
		const lines = subagentsOf(rows).map((row) => subagentLine(row, model.delegates, NOW));
		// Each delegate names its subagent's transcript by the ref the Board's
		// child row carries (demoFleet.ts).
		expect(model.delegates?.find((delegate) => delegate.delegateId === "g-settle")?.transcriptRef).toBe(
			`local:${demoSessionId("g-settle")}`,
		);
		expect(lines).toContainEqual(
			expect.objectContaining({
				title: "Fix race in tree settle",
				state: "failed",
				activity: "Failed: go test exited 1 (3 times)",
			}),
		);
	});

	it("frames 13 and 14: frame 7's session names its model and effort from a catalog with two providers", () => {
		const { model } = open("s-pr2138");
		expect(modelChipLabel(model, DEMO_MODEL_LIST.data)).toBe("DeepSeek 4.1 Flash · XHigh");
		expect(new Set(DEMO_MODEL_LIST.data.map((entry) => entry.provider)).size).toBe(2);
		expect(DEMO_MODEL_LIST.recent?.length).toBeGreaterThan(0);
		expect(model.reasoningEffortLevels.length).toBeGreaterThan(1);
		expect(model.contextWindow).toBeGreaterThan(model.contextUsed);
		expect(model.diagnostics?.plugins?.length).toBeGreaterThan(0);
	});

	it("frame 8: a question with two questions, one multi-select and one option recommended", () => {
		const { model, rows } = open("s-audit");
		expect(sessionStateLine(model, NOW).text).toBe("Asks a question");
		// The Board's row and a long-press preview read the first question here.
		expect(threadOf("s-audit").evener.pendingQuestion).toEqual({
			question: "Keep or drop the implied options?",
			options: ["Drop them", "Keep them and add the flags", "Ask me per tool"],
			count: 2,
		});
		const questions = [...liveAsksFor(model).values()].flat();
		expect(questions.map((question) => question.question)).toEqual([
			"Keep or drop the implied options?",
			"Which tool groups should I audit next?",
		]);
		expect(questions.map((question) => question.multiSelect)).toEqual([false, true]);
		expect(questions[0]?.options.filter((option) => option.recommended).map((option) => option.label)).toEqual([
			"Drop them",
		]);
		// The dock is the question while it is open, so the transcript leaves it out.
		expect(rows.some((row) => row.kind === "question")).toBe(false);
	});

	it("frame 9: an approval to write outside the workspace, spelled out absolutely", () => {
		const { model } = open("s-mirror");
		expect(sessionStateLine(model, NOW).text).toBe("Asks for approval");
		expect(model.pendingEscalations).toEqual([
			expect.objectContaining({
				tool: "write_file",
				deniedPath: "/home/jesse/sites/docs/index.html",
				ref: fleetSessionRef("s-mirror"),
			}),
		]);
	});

	it("frame 10: a working session whose one queued message offers Steer now", () => {
		const { model } = open("s-tasklist");
		expect(model.status.type).toBe("active");
		const queued = ghosts(model, [], null, []);
		expect(queued).toHaveLength(1);
		expect(queued[0]).toMatchObject({ state: "queued", buttons: ["steerNow"] });
	});

	it("queues enough on one working session to open the Queue sheet from 'N more queued'", () => {
		const { model } = open("s-stumble");
		expect(model.status.type).toBe("active");
		expect(shownGhosts(ghosts(model, [], null, [])).moreQueued).toBeGreaterThan(0);
	});

	it("frame 11: the last turn failed on a sign-in error", () => {
		const { model, conversation } = open("s-retry");
		expect(sessionStateLine(model, NOW).text).toBe("Failed");
		const cause = { kind: "provider", provider: "codex-jesse-fsck.com", model: "gpt-5.6", status: 401 };
		expect(threadOf("s-retry").evener.failure).toEqual({ title: "codex-jesse-fsck.com sign-in expired (401)", cause });
		expect(model.turns.at(-1)?.error).toMatchObject({ cause });
		expect(model.turns.at(-1)).toMatchObject({ status: "failed", error: { message: expect.stringContaining("401") } });
		expect(conversation.items).toContainEqual(
			expect.objectContaining({ kind: "failure", title: expect.stringContaining("401") }),
		);
	});

	it("frame 12: at Tools, an edit shows a diff and a shell step has 60 lines to show all of", () => {
		const { rows } = open("s-jobdisp", "tools");
		const steps = runsOf(rows).flatMap((run) => run.steps);
		const edit = steps.find((step) => step.label === "edit_file");
		const shell = steps.find((step) => step.label === "shell");
		if (!edit || !shell) throw new Error("frame 12 needs an edit and a shell step");
		expect(stepEvidence(edit)).toEqual([expect.objectContaining({ kind: "diff" })]);
		const [output] = stepEvidence(shell);
		expect(output).toMatchObject({ kind: "output", lines: 60 });
		// More lines than the transcript previews, so the step offers "Show all 60 lines".
		expect(EVIDENCE_PREVIEW_LINES).toBeLessThan(60);
	});

	it("advertises a live daemon's capabilities, with Send and Clear only while it rests", () => {
		const daemon = {
			steer: true,
			interrupt: true,
			compact: true,
			forkFromTurn: false,
			shutdown: true,
			changeModel: true,
			changeVisionModel: true,
			queue: true,
			goal: true,
			sharedNotes: true,
			rename: true,
			skillInput: true,
		};
		expect(threadOf("s-pr2138").evener.capabilities).toEqual({ ...daemon, send: false, clear: false });
		expect(threadOf("s-diff").evener.capabilities).toEqual({ ...daemon, send: true, clear: true });
		// A pending question or approval blocks Clear, as the daemon's does.
		expect(threadOf("s-audit").evener.capabilities).toEqual({ ...daemon, send: true, clear: false });
	});

	it("advertises the hub's past-session capabilities on a shut-down session", () => {
		// cmd/evener-hub/app_threadread.go's pastThreadCapabilities.
		expect(threadOf("s-roster").evener.capabilities).toEqual({
			send: true,
			steer: false,
			interrupt: false,
			compact: true,
			clear: true,
			forkFromTurn: true,
			shutdown: true,
			changeModel: true,
			changeVisionModel: true,
			queue: true,
			goal: true,
			sharedNotes: true,
			rename: true,
			skillInput: true,
		});
	});

	it("advertises only readable notes on a session that needs a restart", () => {
		// cmd/evener-hub/internal/appsource/local_daemon.go, ThreadStatusRestartRequired.
		expect(threadOf("s-namer").evener.capabilities).toEqual({
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
			sharedNotes: true,
			rename: false,
			skillInput: false,
		});
	});

	it("frame 13a: a shut-down session keeps read-only notes and a link", () => {
		const { model } = open("s-roster");
		expect(sessionStateLine(model, NOW).text).toBe("Shut down");
		expect(model.capabilities.sharedNotes).toBe(true);
		// Readable, and read-only: the Notes sheet offers no editing here, as it
		// does on frame 7's working session.
		expect(canWriteHumanNote(model)).toBe(false);
		expect(canWriteHumanNote(open("s-pr2138").model)).toBe(true);
		expect(notesBarPreview(model)).toEqual({
			glyph: "person",
			text: "Your note: Measure on magic-kingdom, not a laptop.",
			links: "1 link",
		});
	});
});
