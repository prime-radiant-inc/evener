import { hydrateThread, makeTranscriptDisplayConfig, parseSteeringNotifications } from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { expect, it } from "vitest";
import { projectTimeline, type MobileTimelineItem } from "./projectedRows";
import {
	groupTimeline,
	isCriticalNotice,
	isInterruptedNotice,
	noticeLabel,
	timelineGap,
	type TimelineRow,
} from "./timeline";

const setup: MobileTimelineItem = {
	kind: "notice",
	id: "setup",
	origin: "system",
	family: "hidden-instruction",
	tone: "system",
	text: "instructions",
};
const diagnostic: MobileTimelineItem = {
	...setup,
	id: "diagnostic",
	family: "diagnostic",
	text: "details",
};
const message: MobileTimelineItem = {
	kind: "user",
	id: "message",
	text: "task",
};
const warning: MobileTimelineItem = {
	...setup,
	id: "warning",
	family: "warning",
	tone: "warning",
};

it("uses tighter rhythm around routine details without compressing warnings or decisions", () => {
	const ordinary = timelineGap(message, message);
	const details = groupTimeline([setup])[0];
	if (!details) throw new Error("missing details");
	expect(timelineGap(message, details)).toBeLessThan(ordinary);
	expect(timelineGap(details, message)).toBeLessThan(ordinary);
	expect(timelineGap(details, warning)).toBe(ordinary);
	expect(timelineGap(warning, details)).toBe(ordinary);
	expect(
		timelineGap(details, {
			kind: "failure",
			id: "failed",
			title: "Failed",
			detail: "reason",
		}),
	).toBe(ordinary);
	expect(timelineGap(details, undefined)).toBe(0);
});

it("treats a run and a time marker as routine, like details", () => {
	const ordinary = timelineGap(message, message);
	const run: TimelineRow = { kind: "run", id: "run:a", steps: [] };
	const time: TimelineRow = { kind: "time", id: "time:turn_1", turnId: "turn_1", at: 0 };
	expect(timelineGap(message, run)).toBeLessThan(ordinary);
	expect(timelineGap(run, message)).toBeLessThan(ordinary);
	expect(timelineGap(message, time)).toBeLessThan(ordinary);
	expect(timelineGap(time, message)).toBeLessThan(ordinary);
});

it("treats a saved note as routine, like a run or a time marker (spec 8.8)", () => {
	const ordinary = timelineGap(message, message);
	const note: TimelineRow = { kind: "note", id: "note:a", text: "Fix causes" };
	expect(timelineGap(message, note)).toBeLessThan(ordinary);
	expect(timelineGap(note, message)).toBeLessThan(ordinary);
});

// Intent replaces a folded run with one line per action, so those lines keep
// the run's rhythm instead of standing apart like prose. A failed action is
// attention, not a routine line (Jesse, 2026-10-18).
it("treats a settled intent line as routine, and a failed one as attention", () => {
	const ordinary = timelineGap(message, message);
	const intentLine: TimelineRow = {
		kind: "activity",
		id: "a",
		label: "read_file",
		family: "tool",
		state: "completed",
		summaryOnly: true,
		detail: { description: "Read the runner" },
	};
	expect(timelineGap(message, intentLine)).toBeLessThan(ordinary);
	expect(timelineGap(intentLine, message)).toBeLessThan(ordinary);
	expect(timelineGap(intentLine, { ...intentLine, id: "b" })).toBeLessThan(ordinary);
	expect(timelineGap(message, { ...intentLine, id: "c", state: "failed" })).toBe(ordinary);
});

it("collapses only typed non-warning interruption notices", () => {
	const interrupted = {
		...setup,
		origin: "steering" as const,
		steeringKind: "interrupted",
	};
	const interruptedSalvage = {
		...interrupted,
		steeringKind: "interrupted-salvage" as const,
	};
	expect(isInterruptedNotice(interrupted)).toBe(true);
	expect(isInterruptedNotice(interruptedSalvage)).toBe(true);
	expect(isInterruptedNotice({ ...interrupted, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interruptedSalvage, tone: "warning" })).toBe(false);
	expect(isInterruptedNotice({ ...interrupted, origin: "system" })).toBe(false);
	expect(
		isInterruptedNotice({
			...interrupted,
			steeringKind: undefined,
			text: "Interrupted",
		}),
	).toBe(false);
	expect(isInterruptedNotice({ ...interrupted, steeringKind: "notification" })).toBe(false);
});

it("groups consecutive internal entries without losing order or contents", () => {
	const input = [setup, diagnostic, message, { ...setup, id: "later" }];
	const rows = groupTimeline(input);
	expect(rows).toHaveLength(3);
	expect(rows[0]).toMatchObject({
		kind: "details",
		entries: [setup, diagnostic],
	});
	expect(rows[1]).toBe(message);
	expect(rows.flatMap((row) => (row.kind === "details" ? row.entries : [row]))).toEqual(input);
	expect(input).toHaveLength(4);
});

it("keeps warnings visible even when classified as internal details", () => {
	const internalWarning = {
		...diagnostic,
		id: "internal-warning",
		tone: "warning" as const,
	};
	expect(groupTimeline([setup, warning, internalWarning, diagnostic])).toMatchObject([
		{ kind: "details", entries: [setup] },
		warning,
		internalWarning,
		{ kind: "details", entries: [diagnostic] },
	]);
});

it("retains a disclosure identity as adjacent details arrive", () => {
	expect(groupTimeline([setup])[0]?.id).toBe(groupTimeline([setup, diagnostic])[0]?.id);
	expect(groupTimeline([])).toEqual([]);
});

it("folds a labelled notice to its label, unless it is critical", () => {
	const notice = {
		...setup,
		family: "informational" as const,
		origin: "steering" as const,
		steeringKind: "tasks-done",
		label: "Tasks complete",
	};
	expect(noticeLabel(notice)).toBe("Tasks complete");
	expect(timelineGap(message, notice)).toBeLessThan(timelineGap(message, message));
	expect(noticeLabel({ ...notice, tone: "warning" })).toBeUndefined();
	expect(noticeLabel({ ...notice, eventKind: "error" })).toBeUndefined();
	expect(noticeLabel({ ...notice, label: undefined })).toBeUndefined();
});

// A tool repair is a quiet system event (spec 8.2): the call ran, corrected.
it("never reads a tool repair as critical", () => {
	expect(isCriticalNotice({ ...setup, origin: "system" as const, eventKind: "tool_repair" })).toBe(false);
});

it.each([{ eventKind: "error" }, { eventKind: "hook_completed", exitCode: 3 }, { family: "warning" as const }])(
	"keeps typed critical notices outside collapsed diagnostic groups: %j",
	(metadata) => {
		const critical = { ...diagnostic, ...metadata };
		expect(groupTimeline([setup, critical])).toEqual([
			{ kind: "details", id: "details:setup", entries: [setup] },
			critical,
		]);
	},
);

it("keeps a single quiet_yield standalone and collapses 2+ consecutive quiet_yields into one group", () => {
	const quiet = (id: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "quiet_yield",
		text: "quiet-sentinel",
	});
	expect(groupTimeline([message, quiet("q1")])).toEqual([message, quiet("q1")]);

	const grouped = groupTimeline([setup, quiet("q1"), quiet("q2"), diagnostic]);
	expect(grouped).toMatchObject([
		{ kind: "details", id: "details:setup", entries: [setup] },
		{ kind: "details", id: "quiet-yields:q1", entries: [quiet("q1"), quiet("q2")] },
		{ kind: "details", id: "details:diagnostic", entries: [diagnostic] },
	]);
	expect((grouped[1] as Extract<TimelineRow, { kind: "details" }>).summary).toBeTruthy();
	expect(groupTimeline([quiet("q1"), quiet("q2")])[0]?.id).toBe(
		groupTimeline([quiet("q1"), quiet("q2"), quiet("q3")])[0]?.id,
	);
});

it("groups consecutive notification-bearing quiet wakes while keeping single wakes and action turns separate", () => {
	const notif = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "steering",
		steeringKind: "notification",
		family: "informational",
		tone: "info",
		text: `<job-notification job_id="${id}" event="progress">working</job-notification>`,
		notifications: [{ kind: "text", text: "working" }],
	});
	const quiet = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "quiet_yield",
		text: "quiet-sentinel",
	});
	const action: MobileTimelineItem = {
		kind: "activity",
		id: "act_1",
		turnId: "turn_action",
		label: "shell",
		family: "tool",
		state: "completed",
		detail: {},
	};

	// A single notification + quiet wake stays standalone.
	expect(groupTimeline([notif("n0", "t0"), quiet("q0", "t0")])).toHaveLength(2);

	// An action turn's trailing quiet notice stays standalone; two pure quiet wakes group together.
	const grouped = groupTimeline([
		action,
		quiet("qa", "turn_action"),
		notif("n1", "t1"),
		quiet("q1", "t1"),
		notif("n2", "t2"),
		quiet("q2", "t2"),
	]);
	expect(grouped).toMatchObject([
		action,
		quiet("qa", "turn_action"),
		{
			kind: "details",
			id: "quiet-yields:n1",
			entries: [notif("n1", "t1"), quiet("q1", "t1"), notif("n2", "t2"), quiet("q2", "t2")],
		},
	]);
});

it("breaks quiet-yield grouping on failed job notifications while grouping routine wakes", () => {
	const failedRaw = notificationWireItem("job-shell-failed").text ?? "";
	const completedRaw = notificationWireItem("job-shell-completed").text ?? "";
	const failedNotif: MobileTimelineItem = {
		kind: "notice",
		id: "nf",
		turnId: "t_fail",
		origin: "steering",
		steeringKind: "notification",
		family: "informational",
		tone: "info",
		text: failedRaw,
		notifications: parseSteeringNotifications(failedRaw),
	};
	const completedNotif = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "steering",
		steeringKind: "notification",
		family: "informational",
		tone: "info",
		text: completedRaw,
		notifications: parseSteeringNotifications(completedRaw),
	});
	const quiet = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "quiet_yield",
		text: "quiet-sentinel",
	});
	const rows = groupTimeline([
		failedNotif,
		quiet("qf", "t_fail"),
		completedNotif("n1", "t1"),
		quiet("q1", "t1"),
		completedNotif("n2", "t2"),
		quiet("q2", "t2"),
	]);
	expect(rows).toMatchObject([
		failedNotif,
		quiet("qf", "t_fail"),
		{
			kind: "details",
			id: "quiet-yields:n1",
			entries: [completedNotif("n1", "t1"), quiet("q1", "t1"), completedNotif("n2", "t2"), quiet("q2", "t2")],
		},
	]);
});

it.each(["intent", "tools", "full"] as const)(
	"folds settled healthy reasoning into quiet-yield groups at %s while keeping live/failed reasoning separate",
	(level) => {
		const wake = (id: string, status: "completed" | "failed" | "inProgress" = "completed") => ({
			id,
			itemsView: "full" as const,
			status,
			items: [
				{
					id: `r_${id}`,
					turnId: id,
					type: "reasoning" as const,
					text: `think-${id}`,
					status:
						status === "failed"
							? ("failed" as const)
							: status === "inProgress"
								? ("inProgress" as const)
								: ("completed" as const),
				},
				...(status === "failed"
					? []
					: [
							{
								id: `qy_${id}`,
								turnId: id,
								type: "systemMessage" as const,
								text: "quiet-sentinel",
								eventKind: "quiet_yield" as const,
							},
						]),
			],
		});
		const thread = wireThread("local:test", {
			turns: [
				wake("t1"),
				wake("t2"),
				wake("t_fail", "failed"),
				{
					id: "t_live",
					itemsView: "full",
					status: "inProgress",
					items: [
						{
							id: "qy_t_live",
							turnId: "t_live",
							type: "systemMessage",
							text: "quiet-sentinel",
							eventKind: "quiet_yield" as const,
						},
						{
							id: "r_t_live",
							turnId: "t_live",
							type: "reasoning",
							text: "think-live",
							status: "inProgress",
						},
					],
				},
			],
		});
		const model = hydrateThread({ thread }, "local:test", 0);
		const projected = projectTimeline(model, new Map(), makeTranscriptDisplayConfig({ kind: "preset", level }));
		const rows = groupTimeline(projected);
		expect(rows).toMatchObject([
			{ kind: "details", id: "quiet-yields:r_t1" },
			{ turnId: "t_fail" },
			{ id: "qy_t_live", turnId: "t_live" },
			{ id: "r_t_live", turnId: "t_live", state: "running" },
		]);
		const group = rows[0] as Extract<TimelineRow, { kind: "details" }>;
		expect(group.entries.map((entry) => entry.id)).toEqual(["r_t1", "qy_t1", "r_t2", "qy_t2"]);
	},
);

it("folds quiet wakes carrying round_timings and routine hooks while breaking on warnings, errors, and tool actions", () => {
	const quiet = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "quiet_yield",
		text: "quiet-sentinel",
	});
	const timing = (id: string, turnId: string): MobileTimelineItem => ({
		kind: "notice",
		id,
		turnId,
		origin: "system",
		family: "diagnostic",
		tone: "system",
		eventKind: "round_timings",
		text: "120ms",
	});
	const hookOk: MobileTimelineItem = {
		kind: "notice",
		id: "hk_ok",
		turnId: "t1",
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "hook_completed",
		exitCode: 0,
		text: "Hook ok",
	};
	const hookFail: MobileTimelineItem = {
		...hookOk,
		id: "hk_fail",
		turnId: "t_fail",
		exitCode: 1,
	};
	const repair: MobileTimelineItem = {
		kind: "notice",
		id: "rep_1",
		turnId: "t_rep",
		origin: "system",
		family: "lifecycle",
		tone: "system",
		eventKind: "tool_repair",
		text: "Repaired call",
	};
	const rows = groupTimeline([
		hookOk,
		quiet("q1", "t1"),
		timing("rt1", "t1"),
		quiet("q2", "t2"),
		timing("rt2", "t2"),
		hookFail,
		quiet("qf", "t_fail"),
		repair,
		quiet("qr", "t_rep"),
	]);
	expect(rows).toMatchObject([
		{ kind: "details", id: "quiet-yields:hk_ok", summary: "2 quiet yields" },
		hookFail,
		quiet("qf", "t_fail"),
		repair,
		quiet("qr", "t_rep"),
	]);
	const group = rows[0] as Extract<TimelineRow, { kind: "details" }>;
	expect(group.entries.map((entry) => entry.id)).toEqual(["hk_ok", "q1", "rt1", "q2", "rt2"]);
});
