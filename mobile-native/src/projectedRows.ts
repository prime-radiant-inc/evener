// The row adapter between the shared transcript projector and the phone's
// timeline rows.
//
// The package's projectThread (@evener/appwire-client) classifies a thread's
// turns into ProjectedEntry kinds (item | thinking | intent | critical). This
// file maps each of those kinds onto the native MobileTimelineItem union the
// phone already renders, reproducing field for field what
// mobile/src/conversation/project.ts builds today for the same item — its
// projectItem, warningItem and failureItem — so the ~23 existing consumers
// compile and behave unchanged when a later slice (D24-3) swaps
// projectTimeline for projectThread + this adapter.
//
// D24 slice 1 of 6: this adapter lands BEFORE its consumer. Nothing in
// production wires it up yet; the tests in projectedRows.test.ts are the
// deliverable, pinning the mapping against the current native row shape. The
// adapter is deliberately native-private (no row type moves into the package):
// native's notice tone/family vocabulary has no projector counterpart, and its
// clustering pass, attachment rows and truncation caps all stay native-side
// (project.ts), not this file's concern.
//
// Forward-compatibility is load-bearing here exactly as it is in project.ts:
// an unknown ItemModel.type never disappears; it becomes a neutral collapsed
// activity row. Every string on a row is untrusted plain text.

import {
	type AskQuestionRef,
	hasItemFailure,
	hasWarningText,
	isActiveItem,
	joinedReasoningParagraphs,
	joinWarningParts,
	parseAskUserQuestions,
	pendingTextJoined,
	type ItemModel,
	type ProjectedEntry,
} from "@evener/appwire-client";
import type {
	ActivityDetail,
	ActivityState,
	MobileTimelineItem,
	NoticeFamily,
	NoticeTone,
} from "../../mobile/src/conversation/project";

// What a row needs beyond the entry itself. The projector's ProjectedEntry
// carries its source item, but not the two things project.ts reads from the
// wider model while building the same rows:
//   - turnStatus: an item with no status of its own is live exactly while its
//     turn is (isActiveItem), so a running/completed activity depends on it;
//   - asks: only an ask_user call that is still answerable becomes a question
//     row (liveAsksFor). An absent map means "nothing answerable", which keeps
//     this function pure and gives every call site the same behavior.
export interface ProjectedRowContext {
	readonly turnStatus?: string;
	readonly asks?: ReadonlyMap<string, AskQuestionRef[]>;
}

// One ProjectedEntry becomes one row (or none, for a warning with nothing to
// show). Attachments emitted alongside a source row are project.ts's concern,
// not this mapping's — the row kinds here are the four the projector emits.
export function projectedRow(
	entry: ProjectedEntry,
	context: ProjectedRowContext = {},
): MobileTimelineItem | null {
	switch (entry.kind) {
		case "item":
			return rowForItem(entry.item, context);
		case "intent":
			// An intent is the same tool activity row; the projector's config
			// decides intent-vs-full, and native's presentation layer (the
			// ActivityPresentation "intent" mode) renders the difference. The
			// row shape carries no mode field, so this is identical to a tool
			// item and stays that way.
			return rowForItem(entry.item, context);
		case "thinking":
			// A content-free placeholder: the reader sees that the agent is
			// thinking without the thought's stream. Never the thought text.
			return { ...reasoningPlaceholderRow(entry.item), ...itemIdentity(entry.item) };
		case "critical":
			if (entry.item.type === "reasoning") return criticalReasoningRow(entry);
			return rowForItem(entry.item, context);
	}
}

// --- per-item row construction (mirrors project.ts's projectItem) -----------

function rowForItem(it: ItemModel, context: ProjectedRowContext): MobileTimelineItem | null {
	const identity = itemIdentity(it);

	// Human steering shares user input's presentation.
	if (it.type === "userMessage" || (it.type === "steering" && it.source === "user")) {
		return {
			kind: "user",
			id: it.id,
			text: it.text,
			...(it.type === "userMessage" && it.transcriptEntryIndex !== undefined
				? { transcriptEntryIndex: it.transcriptEntryIndex }
				: {}),
			...identity,
		};
	}

	if (it.type === "agentMessage") {
		return {
			kind: "assistant",
			id: it.id,
			markdown: itemMarkdown(it),
			streaming: isActiveItem(it, context.turnStatus),
			...identity,
		};
	}

	// Reasoning renders as a collapsed activity, never "failed" (project.ts's
	// own note: the web never routes a reasoning item to a render failure).
	if (it.type === "reasoning") {
		const state: ActivityState = isActiveItem(it, context.turnStatus) ? "running" : "completed";
		return {
			kind: "activity",
			id: it.id,
			label: "Reasoning",
			family: "reasoning",
			state,
			detail: { ...activityDetail(it), output: reasoningText(it) },
			...identity,
		};
	}

	// A still-answerable ask_user call becomes its interactive question card.
	const question = questionRow(it, context.asks);
	if (question !== null) return { ...question, ...identity };

	if (it.type === "commandExecution") {
		return {
			kind: "activity",
			id: it.id,
			label: toolLabel(it),
			family: "tool",
			state: activityState(it, context.turnStatus),
			detail: activityDetail(it),
			...identity,
		};
	}

	if (it.type === "steering") return { ...steeringNotice(it), ...identity };
	if (it.type === "systemMessage") return { ...systemNotice(it), ...identity };

	if (it.type === "warning") {
		const failure = warningFailure(it);
		return failure === null ? null : { ...failure, ...identity };
	}

	// Unknown / forward-compatible type: a neutral collapsed activity that
	// never disappears and never exposes raw HTML.
	const state = activityState(it, context.turnStatus);
	return {
		kind: "activity",
		id: it.id,
		label: "Activity",
		family: "unknown",
		state,
		detail: { ...activityDetail(it), output: it.text || it.output },
		...identity,
	};
}

// A content-free activity row for a live current thought whose text the config
// hides: label and liveness only, no detail body.
function reasoningPlaceholderRow(
	it: ItemModel,
): Extract<MobileTimelineItem, { kind: "activity" }> {
	return {
		kind: "activity",
		id: it.id,
		label: "Reasoning",
		family: "reasoning",
		state: "running",
		detail: {},
	};
}

// A critical reasoning item is always redacted when it reaches here (the
// projector only routes a reasoning item to critical while the `reasoning`
// flag is off). It shows a neutral summary, never the thought.
function criticalReasoningRow(
	entry: Extract<ProjectedEntry, { kind: "critical" }>,
): MobileTimelineItem {
	const it = entry.item;
	const identity = itemIdentity(it);
	if (entry.redacted) {
		return { kind: "failure", id: it.id, title: entry.summary, detail: "", ...identity };
	}
	// Defensive: a critical reasoning that is not redacted keeps its thought as
	// a failed reasoning activity.
	return {
		kind: "activity",
		id: it.id,
		label: "Reasoning",
		family: "reasoning",
		state: "failed",
		detail: { ...activityDetail(it), output: reasoningText(it) },
		...identity,
	};
}

// --- item helpers (faithful copies of project.ts's own) ---------------------

function isAskUser(it: ItemModel): boolean {
	return it.type === "commandExecution" && it.toolName === "ask_user";
}

function itemIdentity(it: ItemModel): {
	transcriptKey?: string;
	position?: ItemModel["position"];
} {
	return {
		...(it.transcriptKey ? { transcriptKey: it.transcriptKey } : {}),
		...(it.position ? { position: it.position } : {}),
	};
}

// The settled text plus any in-flight delta chunks a live reducer accumulated.
function itemMarkdown(it: ItemModel): string {
	return it.pendingText ? it.text + pendingTextJoined(it.pendingText) : it.text;
}

// A reasoning item's text as the reader sees it: the longer of the settled
// text and the joined delta summaries wins (see project.ts's reasoningText).
function reasoningText(it: ItemModel): string {
	const joined = joinedReasoningParagraphs(it.reasoningSummaries).join("\n\n");
	return joined.length > it.text.length ? joined : it.text || joined;
}

function toolLabel(it: ItemModel): string {
	return it.toolName ?? it.description?.trim() ?? "Tool";
}

function activityState(it: ItemModel, turnStatus: string | undefined): ActivityState {
	if (hasItemFailure(it)) return "failed";
	if (isActiveItem(it, turnStatus)) return "running";
	return "completed";
}

function activityDescription(it: ItemModel): string | undefined {
	if (it.description?.trim() || !isAskUser(it)) return it.description;
	const questions = parseAskUserQuestions(it);
	if (!questions) return it.description;
	return `Questions: ${questions
		.map((question, index) => question.header.trim() || `Question ${index + 1}`)
		.join("; ")}`;
}

function itemDurationMs(it: ItemModel): number | undefined {
	if (it.startedAt === undefined || it.completedAt === undefined) return undefined;
	return Date.parse(it.completedAt) - Date.parse(it.startedAt);
}

function activityDetail(it: ItemModel): ActivityDetail {
	return {
		description: activityDescription(it),
		arguments: it.argumentsJSON,
		output: it.output,
		error: it.error,
		exitCode: it.exitCode,
		durationMs: itemDurationMs(it),
		callId: it.callId,
	};
}

// The pending ask_user questions of one call, as its interactive card. Each
// ref carries the call's own id (AskQuestionRef.callId), the same grouping
// liveAsksFor performs.
function questionRow(
	it: ItemModel,
	asks: ReadonlyMap<string, AskQuestionRef[]> | undefined,
): Extract<MobileTimelineItem, { kind: "question" }> | null {
	if (!isAskUser(it)) return null;
	const questions = asks?.get(it.callId ?? it.id);
	if (!questions?.length) return null;
	return { kind: "question", id: it.id, questions };
}

// --- notice rows (project.ts's steering/system tables, kept native) ---------

const WARNING_STEERING_KINDS = new Set([
	"loop-detected",
	"turn-limit",
	"provider-failure",
]);

const WARNING_EVENT_KINDS = new Set(["loop_detection", "turn_limit", "error"]);
const HIDDEN_EVENT_KINDS = new Set(["system_prompt", "prompt_loaded"]);
const PRELUDE_EVENT_KINDS = new Set(["environment"]);
const DIAGNOSTIC_EVENT_KINDS = new Set(["round_timings"]);
const LIFECYCLE_EVENT_KINDS = new Set([
	"plugin_loaded",
	"skill_activated",
	"hook_completed",
	"context_compaction",
	"compaction",
	"goal_ended",
	"fork_summary",
	"tool_repair",
	"model_switch",
]);

function systemFamily(eventKind: string | undefined): NoticeFamily {
	if (eventKind && WARNING_EVENT_KINDS.has(eventKind)) return "warning";
	if (eventKind && HIDDEN_EVENT_KINDS.has(eventKind)) return "hidden-instruction";
	if (eventKind && PRELUDE_EVENT_KINDS.has(eventKind)) return "system-prelude";
	if (eventKind && DIAGNOSTIC_EVENT_KINDS.has(eventKind)) return "diagnostic";
	if (eventKind && LIFECYCLE_EVENT_KINDS.has(eventKind)) return "lifecycle";
	return "unknown-system";
}

function steeringNotice(
	it: ItemModel,
): Extract<MobileTimelineItem, { kind: "notice" }> {
	const tone: NoticeTone =
		it.steeringKind && WARNING_STEERING_KINDS.has(it.steeringKind) ? "warning" : "info";
	return {
		kind: "notice",
		id: it.id,
		origin: "steering",
		steeringKind: it.steeringKind,
		family: tone === "warning" ? "warning" : "informational",
		tone,
		text: it.text,
	};
}

function systemNotice(
	it: ItemModel,
): Extract<MobileTimelineItem, { kind: "notice" }> {
	const tone: NoticeTone =
		it.eventKind && WARNING_EVENT_KINDS.has(it.eventKind) ? "warning" : "system";
	return {
		kind: "notice",
		id: it.id,
		origin: "system",
		family: systemFamily(it.eventKind),
		tone,
		text: it.text,
		...(it.eventKind ? { eventKind: it.eventKind } : {}),
		...(it.exitCode !== undefined ? { exitCode: it.exitCode } : {}),
	};
}

// A warning's attention row, or null when it carries nothing to show (the web
// renders no row for exactly that case).
function warningFailure(
	it: ItemModel,
): Extract<MobileTimelineItem, { kind: "failure" }> | null {
	if (joinWarningParts([it.warning?.title, it.text, it.warning?.hint]) === "") return null;
	const rawTitle = it.warning?.title;
	const title = hasWarningText(rawTitle) ? rawTitle : "Warning";
	return { kind: "failure", id: it.id, title, detail: joinWarningParts([it.text, it.warning?.hint]) };
}
