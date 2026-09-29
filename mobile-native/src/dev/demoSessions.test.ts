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
import { displayForLevel } from "../session/detailLevels.js";
import { EVIDENCE_PREVIEW_LINES, stepEvidence } from "../session/evidence.js";
import { ghosts, shownGhosts } from "../session/ghosts.js";
import { modelChipLabel, notesSummary } from "../session/sessionFacts.js";
import { documentReferences } from "../reader/documentReferences.js";
import { canWriteHumanNote, notesBarPreview } from "../session/sessionNotes.js";
import { contextChips, sessionStateLine } from "../session/sessionState.js";
import { subagentLine } from "../session/subagentLine.js";
import { runSummary, runSummaryText, sessionRows } from "../session/transcriptRows.js";
import { createDemoFleet, demoSessionId, fleetSessionRef, fleetSessions } from "./demoFleet.js";
import { createDemoSessions } from "./demoSessions.js";
import { DEMO_MODEL_LIST } from "./demoSetup.js";

const NOW = Date.parse("2026-09-28T21:00:00.000Z");
const sessions = createDemoSessions({ now: NOW });

function threadOf(slug: string, from: Thread[] = sessions): Thread {
	const thread = from.find((candidate) => candidate.evener.ref === fleetSessionRef(slug));
	if (!thread) throw new Error(`no demo thread for ${slug}`);
	return thread;
}

const hostOf = (ref: string) => ref.slice(0, ref.indexOf(":"));

// One session as the Session screen sees it at a detail level: the store's
// projection, then the screen's presentation and transcript rows.
function open(slug: string, level: ContentLevel = "intent", from: Thread[] = sessions) {
	const thread = threadOf(slug, from);
	const model = hydrateThread({ thread }, thread.evener.ref, NOW);
	const display = displayForLevel(level, null);
	const config = display.config ?? undefined;
	const conversation = projectConversation(model, liveAsksFor(model), config);
	const presentation = projectNativeTranscript(conversation, config, {
		justTheConversation: display.justTheConversation,
	});
	const rows = sessionRows(groupTimeline(presentation.items), model.turns);
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

	// A real delegate call settles as soon as its launch receipt returns, so a
	// demo that gave the call its subagent's state would hide a row that reads
	// the call instead of the subagent (the transcript audit's first gap).
	it("settles every subagent call at launch with its receipt, as the hub does", () => {
		const calls = sessions.flatMap((thread) =>
			(thread.turns ?? []).flatMap((turn) => turn.items ?? []).filter((item) => item.toolName === "delegate"),
		);
		expect(calls.length).toBeGreaterThan(0);
		for (const call of calls) {
			expect(call.status).toBe("completed");
			expect(JSON.parse(call.output ?? "{}")).toMatchObject({
				delegate_id: expect.any(String),
				transcript_ref: expect.any(String),
			});
		}
	});

	it.each(["s-pr2138", "s-retry", "s-tasklist"])(
		"reads each subagent row in %s at Intent by its subagent's own state, and can open it",
		(slug) => {
			const { model, rows } = open(slug);
			const subagentRows = subagentsOf(rows);
			expect(subagentRows.length).toBeGreaterThan(0);
			for (const row of subagentRows) {
				const delegate = model.delegates?.find((candidate) => candidate.originToolCallId === row.detail.callId);
				const line = subagentLine(row, model.delegates, NOW);
				expect(line.ref).toBe(delegate?.transcriptRef);
				expect(line.stateText).toMatch(/^(running|failed|done) · \d+[smhd]/);
			}
		},
	);

	it("frames 13 and 14: frame 7's session names its model and effort from a catalog with two providers", () => {
		const { model } = open("s-pr2138");
		expect(modelChipLabel(model, DEMO_MODEL_LIST.data)).toBe("DeepSeek 4.1 Flash · XHigh");
		expect(new Set(DEMO_MODEL_LIST.data.map((entry) => entry.provider)).size).toBeGreaterThanOrEqual(2);
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
		const queued = ghosts(model, [], null, [], true);
		expect(queued).toHaveLength(1);
		expect(queued[0]).toMatchObject({ state: "queued", buttons: ["steerNow"] });
	});

	it("queues enough on one working session to open the Queue sheet from 'N more queued'", () => {
		const { model } = open("s-stumble");
		expect(model.status.type).toBe("active");
		expect(shownGhosts(ghosts(model, [], null, [], true)).moreQueued).toBeGreaterThan(0);
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

	it("frame 17: Get PR 2138 Test Clean names the settle-race plan it wrote, so its chip shows with an age", () => {
		const { thread, model } = open("s-pr2138");
		expect(documentReferences(model.turns, thread.cwd)).toContainEqual({
			path: "docs/superpowers/plans/2026-09-25-settle-race.md",
			updatedAt: expect.any(String),
		});
	});

	it("serves a running subagent read-only, and one whose run ended as a past session (ruling 30)", () => {
		// While a subagent runs in its coordinator's process the hub serves it as
		// a read-only alias with no capabilities (cmd/evener-hub/app_rpc.go);
		// once its run ends, as a past session (pastThreadCapabilities).
		const subagent = (title: string) => {
			const thread = sessions.find((candidate) => candidate.evener.parentRef && candidate.name === title);
			if (!thread) throw new Error(`no subagent thread "${title}"`);
			return thread;
		};
		const running = subagent("Check drain ordering in tests");
		expect(running.status.type).toBe("active");
		expect(Object.values(running.evener.capabilities).some(Boolean)).toBe(false);
		expect(subagent("Fix race in tree settle").evener.capabilities).toEqual(threadOf("s-roster").evener.capabilities);
	});

	it("times a running subagent the same in its row, its transcript entry and its own screen", () => {
		// The Subagents list reads evener/jobs/list, the transcript the thread's
		// delegates, and the subagent's screen its own thread's turn: one fact,
		// its run's start, so the three can't disagree.
		const fleet = createDemoFleet({ now: NOW });
		const tree = fleet.answerJobsList({ ref: fleetSessionRef("s-pr2138") }).data as {
			root: { entries: unknown[] };
		};
		const started = new Map<string, string>();
		const visit = (entries: unknown[]) => {
			for (const entry of entries as {
				kind: string;
				delegate?: { childRef: string; runStartedAt: string; child?: { entries: unknown[] } };
			}[]) {
				if (entry.kind === "delegate" && entry.delegate) {
					started.set(entry.delegate.childRef, entry.delegate.runStartedAt);
					if (entry.delegate.child) visit(entry.delegate.child.entries);
				}
			}
		};
		visit(tree.root.entries);
		const running = sessions.filter((thread) => thread.evener.parentRef && thread.status.type === "active");
		expect(running.length).toBeGreaterThan(0);
		const transcript = new Map(
			(threadOf("s-pr2138").evener.diagnostics?.delegates ?? []).map((delegate) => [
				delegate.transcriptRef,
				delegate.runStartedAt,
			]),
		);
		// Get PR 2138 Test Clean's running subagents, each in all three places.
		const compared = running.filter((thread) => started.has(thread.evener.ref));
		expect(compared.length).toBeGreaterThan(20);
		// Nested ones too, such as the subagents a subagent started.
		expect(compared.some((thread) => thread.evener.parentRef !== threadOf("s-pr2138").evener.ref)).toBe(true);
		expect(transcript.size).toBeGreaterThan(0);
		for (const thread of compared) {
			const listed = started.get(thread.evener.ref);
			expect(new Date(thread.evener.activeTurnStartedAt ?? 0).toISOString()).toBe(listed);
			if (transcript.has(thread.evener.ref)) expect(transcript.get(thread.evener.ref)).toBe(listed);
		}
		const inTranscript = compared.filter((thread) => transcript.has(thread.evener.ref));
		expect(inTranscript.length).toBeGreaterThan(0);
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

// EVENER_DEMO_LONG: content longer than any frame, so a screenshot pass
// exercises long questions, approvals and messages, and many rows.
describe("the demo sessions with long content", () => {
	const long = createDemoSessions({ now: NOW, long: true });
	const longThread = (slug: string) => {
		const thread = long.find((candidate) => candidate.evener.ref === fleetSessionRef(slug));
		if (!thread) throw new Error(`no demo thread for ${slug}`);
		return thread;
	};
	const items = (thread: Thread) => (thread.turns ?? []).flatMap((turn) => turn.items ?? []);
	const hydrated = (slug: string) => {
		const thread = longThread(slug);
		return hydrateThread({ thread }, thread.evener.ref, NOW);
	};
	const notifications = (thread: Thread) =>
		items(thread).filter((item) => item.type === "steering" && item.steeringKind === "notification");

	it("asks four questions, the first long, with five long options", () => {
		const model = hydrated("s-audit");
		const questions = [...liveAsksFor(model).values()].flat();
		expect(questions).toHaveLength(4);
		expect(questions[0]?.question.length).toBeGreaterThan(250);
		expect(questions[0]?.why?.length).toBeGreaterThan(600);
		expect(questions[0]?.options).toHaveLength(5);
		expect(longThread("s-audit").evener.pendingQuestion).toMatchObject({ count: 4 });
	});

	it("asks to write to a deep path that may have partly run", () => {
		expect(hydrated("s-mirror").pendingEscalations).toEqual([
			expect.objectContaining({
				deniedPath:
					"/home/jesse/sites/docs/reference/wire/v6/notifications/evener-navigation-invalidated-and-thread-resync-ordering-guarantees/index.html",
				partiallyRan: true,
			}),
		]);
	});

	it("gives the working session a long message from each side, many steps and notifications", () => {
		const shown = items(longThread("s-pr2138"));
		const usual = items(threadOf("s-pr2138"));
		expect(shown.length).toBeGreaterThan(usual.length + 24);
		expect(shown.some((item) => item.type === "userMessage" && (item.text ?? "").length > 500)).toBe(true);
		expect(shown.some((item) => item.type === "agentMessage" && (item.text ?? "").length > 1500)).toBe(true);
		expect(notifications(longThread("s-pr2138"))).not.toHaveLength(0);
	});

	it("leaves every session's usual content alone without the flag", () => {
		expect(notifications(threadOf("s-pr2138"))).toHaveLength(0);
		expect(threadOf("s-audit").evener.pendingQuestion).toMatchObject({ count: 2 });
	});
});

// One session replays the recorded wire corpora (agent/testdata/*wire), so
// every tool family the phone summarizes can be seen and screenshotted in the
// shapes the daemon sends.
describe("the demo session with every tool family", () => {
	const withTools = createDemoSessions({ now: NOW, toolFamilies: true });

	it("is served only when asked for, leaving the mockup's fleet as it is", () => {
		expect(sessions.some((thread) => thread.evener.ref === fleetSessionRef("s-tools"))).toBe(false);
		expect(withTools.some((thread) => thread.evener.ref === fleetSessionRef("s-tools"))).toBe(true);
	});

	it("replays every recorded call and its result, with every family the phone summarizes", () => {
		const { thread, rows } = open("s-tools", "tools", withTools);
		const items = thread.turns?.flatMap((turn) => turn.items ?? []) ?? [];
		const calls = items.filter((item) => item.type === "commandExecution");
		const families = new Set(calls.map((call) => call.toolName));
		for (const family of [
			"shell",
			"read_file",
			"edit_file",
			"web_fetch",
			"use_skill",
			"list_dir",
			"task_list",
			"delegate",
		])
			expect(families).toContain(family);
		// Each announcement has its result beside it, sharing its callId.
		const settledCalls = new Set(calls.filter((call) => call.status !== "inProgress").map((call) => call.callId));
		expect(calls.filter((call) => !settledCalls.has(call.callId))).toEqual([]);
		expect(items.some((item) => item.type === "systemMessage")).toBe(true);
		expect(items.some((item) => item.type === "steering")).toBe(true);
		expect(new Set(items.map((item) => item.id)).size).toBe(items.length);
		expect(runsOf(rows)).not.toEqual([]);
	});

	// The corpora are recorded apart, so a callId one reuses must not fold
	// another's call into it: each callId names one call and its result.
	it("gives each recorded call a callId of its own across the corpora", () => {
		const items = threadOf("s-tools", withTools).turns?.flatMap((turn) => turn.items ?? []) ?? [];
		const byCallId = new Map<string, string[]>();
		for (const item of items)
			if (item.type === "commandExecution" && item.callId)
				byCallId.set(item.callId, [...(byCallId.get(item.callId) ?? []), item.id]);
		const shared = [...byCallId].filter(
			([, ids]) =>
				ids.filter((id) => !id.startsWith("item_tool_result_")).length > 1 ||
				ids.filter((id) => id.startsWith("item_tool_result_")).length > 1,
		);
		expect(shared).toEqual([]);
	});

	// The package folds a call and its result into one step by callId, as a
	// reload serves them, so an edit keeps both its arguments and its output.
	it("folds each recorded call with its result, as the phone reads a reloaded transcript", () => {
		const { model } = open("s-tools", "tools", withTools);
		const steps = model.turns.flatMap((turn) => turn.items).filter((item) => item.type === "commandExecution");
		expect(new Set(steps.map((step) => step.callId)).size).toBe(steps.length);
		const edit = steps.find((step) => step.toolName === "edit_file");
		expect(edit?.argumentsJSON).toContain("old_string");
		expect(edit?.output).toBe("edited agent/tree.go: 1 replacement");
	});
});
