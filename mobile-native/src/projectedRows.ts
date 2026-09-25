// The canonical row module between the shared transcript projector and the
// phone's timeline.
//
// The package's projectThread (@evener/appwire-client) classifies a thread's
// turns into ProjectedEntry kinds (item | thinking | intent | critical); this
// module maps each kind onto the native MobileTimelineItem union the phone
// renders, and folds a whole ThreadModel's turns into the row list the store
// publishes. It is the D24 end state: the pre-shared-projector private family
// (mobile/src/conversation/, deleted in D24-6) is gone, the row vocabulary and
// the timeline projection are re-homed here, and every consumer — the store's
// seam (mobile/src/state/conversation.ts), the service (mobile/src/services/
// conversation.ts) and the ~14 native screens — imports them from this file.
//
// The projection takes the user's transcript display config (D24-5's content
// dimension, routed through the locked seam): which items exist as rows at
// all is the shared projector's decision at that config, so the projection
// runs once, at the user's level, inside the store's display boundary. A
// config-less call keeps the show-everything default (PROJECT_EVERYTHING
// below), exactly what the seam projected before the routing landed.
//
// The operator's summary-only ruling (Sep-25): a summarized tool-action row —
// the projector's "intent" entry — carries ONLY its summary line at compact
// levels (chat/intent); its full detail (arguments, output, error, exit
// code, duration, call id) returns at tools/activity/full, where the
// projector routes the same call through its "item" entry. The row is marked
// summaryOnly so the presentation layer renders the line without an
// expansion affordance.
//
// Two robustness behaviors are canonical here (D24-3 deferred them to the
// re-home): a blank/whitespace tool label falls back to "Tool" instead of "",
// and a duration whose timestamps do not parse is dropped instead of NaN.
// The forward-compatibility rules are load-bearing exactly as before: an
// unknown ItemModel.type never disappears; it becomes a neutral collapsed
// activity row. Every string on a row is untrusted plain text.

import {
	ACTION_SUMMARY_UNAVAILABLE,
	type AskQuestionRef,
	configFingerprint,
	hasItemFailure,
	hasWarningText,
	isActiveItem,
	joinedReasoningParagraphs,
	joinWarningParts,
	liveAskQuestions,
	makeTranscriptDisplayConfig,
	parseAskUserQuestions,
	pendingTextJoined,
	projectThread,
} from "@evener/appwire-client";
import type {
	ItemImage,
	ItemModel,
	ProjectedEntry,
	ThreadModel,
	TranscriptDisplayConfigV1,
	Turn,
	TurnModel,
} from "@evener/appwire-client";

// --- the conversation native holds -------------------------------------------

// The package ThreadModel, as reducer.hydrateThread produces it, plus the
// display rows this module projects from its turns.
export type MobileConversation = ThreadModel & {
	items: MobileTimelineItem[];
};

// --- display rows -------------------------------------------------------------
// Every field is untrusted plain text; only assistant Markdown is sanitized
// later (markdown.ts). Treat string fields as display-only, never executable.

// One image attachment row entry: the package's ItemImage (src is the resolved
// fetch URL, name the wire's own field carried alongside) keyed for display.
export type AttachmentRef = ItemImage & { id: string };

// Lifecycle state of a collapsed activity row (tool call, reasoning, or an
// unknown forward-compatible item). "running" while in progress, "completed"
// on clean settlement, "failed" when the wire carried an error (status stays
// "completed" even for errored calls — error presence is the real signal).
export type ActivityState = "running" | "completed" | "failed";

// Durable activity family discriminator, independent of the display `label`.
// The projection sets this from the item's *type* — commandExecution (tool)
// vs reasoning vs anything else — never from the label string, so a
// commandExecution whose toolName is "Reasoning" is still family "tool" and a
// reasoning item is family "reasoning". Closed type: tool | reasoning | unknown.
// Consumers branch on `family`, never on `label`, so a renamed or localized
// label cannot change an item's family.
export type ActivityFamily = "tool" | "reasoning" | "unknown";

// Expandable detail behind a one-line activity card. Every field is plain
// text — never raw HTML — and may be truncated by the renderer. `arguments`
// is the tool's argumentsJSON verbatim (untrusted JSON text), `output` is the
// tool result text, `error` is the tool-result error text. `callId` lets a
// diagnostics disclosure cite the stable identifier without exposing it in
// the default collapsed row.
export interface ActivityDetail {
	description?: string;
	arguments?: string;
	output?: string;
	error?: string;
	exitCode?: number;
	durationMs?: number;
	callId?: string;
}

export interface ActivityMember {
	id: string;
	label: string;
	family: ActivityFamily;
	state: ActivityState;
	detail: ActivityDetail;
	// Carried from the member's own row when the cluster absorbed a
	// summary-only (intent) row, so the presentation layer renders the
	// expanded member the same way it renders the cluster.
	summaryOnly?: boolean;
	transcriptKey?: string;
	position?: { entry: number; item: number };
}

// Tone of a steering/lifecycle notice row. "info" for ordinary steering/system
// notices, "warning" for loop detection / turn limit / provider failure, and
// "system" for environment / prelude scaffold that is purely informational.
export type NoticeTone = "info" | "warning" | "system";

export type NoticeOrigin = "steering" | "system";

export type NoticeFamily =
	| "informational"
	| "warning"
	| "hidden-instruction"
	| "system-prelude"
	| "lifecycle"
	| "diagnostic"
	| "unknown-system";

// The mobile timeline item union. A pure projection of one thread's turns
// into the families the phone timeline renders. Discriminated by `kind`.
export type MobileTimelineItem = (
	| { kind: "user"; id: string; text: string; transcriptEntryIndex?: number }
	| { kind: "assistant"; id: string; markdown: string; streaming: boolean }
	| {
			kind: "activity";
			id: string;
			label: string;
			// Durable activity-family discriminator, independent of `label`. The
			// projection sets this from the item's type (commandExecution → "tool",
			// reasoning → "reasoning", anything else → "unknown"), never from the
			// label text. Required: every activity constructor MUST set it to a
			// concrete ActivityFamily; consumers branch on `family`, never `label`.
			family: ActivityFamily;
			state: ActivityState;
			detail: ActivityDetail;
			// The operator's summary-only ruling: the row carries ONLY its
			// summary line (detail.description) — nothing to expand. Set on the
			// projector's intent entries; the presentation layer renders the line
			// without an expansion affordance.
			summaryOnly?: boolean;
			members?: ActivityMember[];
		}
	| {
			kind: "notice";
			id: string;
			origin: NoticeOrigin;
			steeringKind?: string;
			eventKind?: string;
			exitCode?: number;
			family: NoticeFamily;
			tone: NoticeTone;
			text: string;
		}
	// The pending ask_user questions of one call, each carrying that call's id
	// (AskQuestionRef.callId); the composer renders them as interactive cards
	// with a single "Send answers" action.
	| { kind: "question"; id: string; questions: AskQuestionRef[] }
	| { kind: "failure"; id: string; title: string; detail: string }
	| { kind: "attachments"; id: string; items: AttachmentRef[] }
) & {
	transcriptKey?: string;
	sourceTranscriptKey?: string;
	position?: { entry: number; item: number };
};

// --- the entry mapping --------------------------------------------------------

// What a row needs beyond the entry itself. The projector's ProjectedEntry
// carries its source item, but not the two things the projection reads from
// the wider model while building the same rows:
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
// show). Attachments emitted alongside a source row are the timeline fold's
// concern (itemAttachments below), not this mapping's — the row kinds here
// are the four the projector emits.
export function projectedRow(
	entry: ProjectedEntry,
	context: ProjectedRowContext = {},
): MobileTimelineItem | null {
	switch (entry.kind) {
		case "item":
			return rowForItem(entry.item, context);
		case "intent":
			// An intent is the projector's summarized tool action: per the
			// operator's summary-only ruling the row carries ONLY its summary
			// line, and the projector's own `failed` classification is honored
			// here rather than re-derived.
			return intentRow(entry, context);
		case "thinking":
			// A content-free placeholder: the reader sees that the agent is
			// thinking without the thought's stream. Never the thought text.
			return { ...reasoningPlaceholderRow(entry.item), ...itemIdentity(entry.item) };
		case "critical":
			if (entry.item.type === "reasoning") return criticalReasoningRow(entry);
			return rowForItem(entry.item, context);
		default:
			return unhandledEntryKind(entry);
	}
}

// Unreachable while ProjectedEntry's union is the four kinds above: a fifth
// kind fails to compile here (never), while returning null keeps the runtime
// contract (MobileTimelineItem | null, never undefined).
function unhandledEntryKind(_entry: never): null {
	return null;
}

// The operator's summary-only ruling: at compact levels (the projector's
// intent entries) a SETTLED tool action carries ONLY its summary line. The
// projector hands its trimmed rationale; the source item's full detail
// (arguments, output, exit code, duration, call id) is dropped — it returns at
// tools/activity/full, where the same call is an "item" entry. The
// "unavailable" placeholder means the source carried no description, so the
// detail is left empty and the presentation layer's own summary fallback (the
// write_file path included) renders the line.
//
// The native attention rule outranks the summarization (D24-4's disclosed
// contract: a failed or running activity renders critical, with its full
// detail, at every level): a failed or still-running call the projector routed
// through its intent entry keeps everything, so the reader can always see why
// a call failed — the ruling covers the settled row.
function intentRow(
	entry: Extract<ProjectedEntry, { kind: "intent" }>,
	context: ProjectedRowContext,
): MobileTimelineItem | null {
	const row = rowForItem(entry.item, context);
	if (row === null || row.kind !== "activity") return row;
	if (entry.failed || row.state !== "completed") {
		return { ...row, state: entry.failed ? "failed" : row.state };
	}
	const detail =
		entry.rationale === ACTION_SUMMARY_UNAVAILABLE
			? {}
			: { description: entry.rationale };
	return {
		...row,
		state: entry.failed ? "failed" : row.state,
		summaryOnly: true,
		detail,
	};
}

// --- per-item row construction -------------------------------------------------

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

	// Reasoning renders as a collapsed activity, never "failed" (the web never
	// routes a reasoning item to a render failure).
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

// --- item helpers ---------------------------------------------------------------

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
// text and the joined delta summaries wins.
function reasoningText(it: ItemModel): string {
	const joined = joinedReasoningParagraphs(it.reasoningSummaries).join("\n\n");
	return joined.length > it.text.length ? joined : it.text || joined;
}

// The canonical blank-label behavior (D24-3's deferred delta): a toolName or
// description that is only whitespace falls through to the next candidate —
// never rendering a blank or whitespace label.
function toolLabel(it: ItemModel): string {
	return it.toolName?.trim() || it.description?.trim() || "Tool";
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

// The canonical duration behavior (D24-3's deferred delta): a duration whose
// timestamps do not parse is dropped (undefined) instead of NaN.
function itemDurationMs(it: ItemModel): number | undefined {
	if (it.startedAt === undefined || it.completedAt === undefined) return undefined;
	const start = Date.parse(it.startedAt);
	const end = Date.parse(it.completedAt);
	if (Number.isNaN(start) || Number.isNaN(end)) return undefined;
	return end - start;
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

// --- image / attachment projection ----------------------------------------------

// Attachment rows keyed by their item and index; the package resolved each
// image's src at hydrate (url, inline bytes, sha route, path, name).
function attachmentRows(
	itemId: string,
	images: ItemImage[] | undefined,
	prefix = "",
): AttachmentRef[] | undefined {
	if (!images?.length) return undefined;
	return images.map((img, i) => ({ id: `${itemId}:${prefix}${i}`, ...img }));
}

// The attachments emitted alongside an item's row: a user message's input
// images (human steering shares the presentation), and a tool call's output
// images. The folded model item (reducer.applyNotification's mergeItemImages)
// carries the "absent/empty input images means unchanged" rule this reads.
function itemAttachments(item: ItemModel): AttachmentRef[] | undefined {
	// Human steering uses the same message and image presentation as user input.
	if (item.type === "userMessage" || (item.type === "steering" && item.source === "user")) {
		return attachmentRows(item.id, item.images);
	}
	if (item.type === "commandExecution") {
		return attachmentRows(item.id, item.outputImages, "out:");
	}
	return undefined;
}

// --- pending ask_user questions --------------------------------------------------

// The package's rule for which ask_user calls are still answerable
// (settled without error, after the last user message), grouped by call so
// each call becomes one question row.
function askQuestionsByCall(model: ThreadModel): Map<string, AskQuestionRef[]> {
	const byCall = new Map<string, AskQuestionRef[]>();
	for (const ref of liveAskQuestions(model)) {
		const questions = byCall.get(ref.callId);
		if (questions) questions.push(ref);
		else byCall.set(ref.callId, [ref]);
	}
	return byCall;
}

// liveAskQuestions has no memory of its own (its own doc comment) — it rescans
// every turn's items and re-parses every pending ask_user's argumentsJson on
// every call, gated on model.askPending (#1731 piece A round 4). Keyed on
// model.turns AND model.askPending, this reuses ONE scan for every caller
// that shares both: projectTimeline's own default argument below, and
// pendingQuestions (mobile-native/src/questionAnswers.ts), which both run
// against the same conversation within one publish. It does not make the scan
// itself incremental — the reducer (reducer.ts's mapTurn/settleFirstMatchingTurn)
// returns a new turns array on every fold, even when only the newest turn
// changed, so a delta still pays for one scan; this removes paying for it twice
// or more within that one delta.
//
// askPending has to be part of the key, not just turns: a status-only frame
// (conversation.ts's changesRows) can flip askPending while handing back the
// SAME turns reference, and a memo keyed on turns alone would serve the
// answer it cached under the OLD flag — an already-projected tool row
// staying stuck instead of becoming its question row, or the reverse.
const asksByTurns = new WeakMap<
	readonly TurnModel[],
	{ askPending: boolean; asks: ReadonlyMap<string, AskQuestionRef[]> }
>();

export function liveAsksFor(
	model: ThreadModel,
): ReadonlyMap<string, AskQuestionRef[]> {
	const cached = asksByTurns.get(model.turns);
	if (cached !== undefined && cached.askPending === model.askPending) {
		return cached.asks;
	}
	const asks = askQuestionsByCall(model);
	asksByTurns.set(model.turns, { askPending: model.askPending, asks });
	return asks;
}

// --- notice rows ------------------------------------------------------------------

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
	if (!eventKind) return "unknown-system";
	if (WARNING_EVENT_KINDS.has(eventKind)) return "warning";
	if (HIDDEN_EVENT_KINDS.has(eventKind)) return "hidden-instruction";
	if (PRELUDE_EVENT_KINDS.has(eventKind)) return "system-prelude";
	if (DIAGNOSTIC_EVENT_KINDS.has(eventKind)) return "diagnostic";
	if (LIFECYCLE_EVENT_KINDS.has(eventKind)) return "lifecycle";
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
	// A system family of "warning" IS the warning tone (systemFamily's own
	// first branch), so the two are derived from one classification.
	const family = systemFamily(it.eventKind);
	const tone: NoticeTone = family === "warning" ? "warning" : "system";
	return {
		kind: "notice",
		id: it.id,
		origin: "system",
		family,
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
	const rawTitle = it.warning?.title;
	// The blank check is exactly "no titled part AND no body part": a warning
	// with nothing to show produces no row (the web renders none either). The
	// body join is computed once and reused.
	const detail = joinWarningParts([it.text, it.warning?.hint]);
	if (!hasWarningText(rawTitle) && detail === "") return null;
	return {
		kind: "failure",
		id: it.id,
		title: hasWarningText(rawTitle) ? rawTitle : "Warning",
		detail,
	};
}

// --- clustering pass --------------------------------------------------------------
// Merge consecutive activity rows that share a cluster family into a single
// activity row keyed by the first member. The cluster state is running if any
// member is running; otherwise completed (failed members never join a run,
// so a cluster is never failed).

interface PreActivity {
	family: string;
	item: Extract<MobileTimelineItem, { kind: "activity" }>;
}

// One run of consecutive same-family activities becomes one row: its first
// member's identity and detail, running if any member runs, with the members
// carried for the renderer that expands them. A run of one is that row itself.
// The caller groups by family (projectTimeline's flushActivityRun), so this is
// handed a homogeneous run and does no regrouping of its own.
function clusterActivityRun(
	run: PreActivity[],
): Extract<MobileTimelineItem, { kind: "activity" }> | undefined {
	const first = run[0]?.item;
	if (first === undefined) return undefined;
	if (run.length === 1) return first;
	const state: ActivityState = run.some((p) => p.item.state === "running")
		? "running"
		: "completed";
	const members: ActivityMember[] = run.map(({ item }) => ({
		id: item.id,
		label: item.label,
		family: item.family,
		state: item.state,
		detail: item.detail,
		...(item.summaryOnly ? { summaryOnly: item.summaryOnly } : {}),
		...(item.transcriptKey ? { transcriptKey: item.transcriptKey } : {}),
		...(item.position ? { position: item.position } : {}),
	}));
	return { ...first, state, members };
}

// --- timeline projection -------------------------------------------------------------

// The config a config-less projection defaults to. Full content with every
// row-relevant advanced gate open is exactly "the projector hides nothing":
// prompt events, round timings, system events and every hook exit all
// survive, so the delegation is invisible to the locked seam's callers that
// have no config yet (the service's projection before the hub's settings
// load, and any host that never supplies one). tokenCounts and estimatedCost
// never gate a row (metadata only), so they stay at their shipped defaults.
const PROJECT_EVERYTHING_CONFIG: TranscriptDisplayConfigV1 = makeTranscriptDisplayConfig(
	{ kind: "preset", level: "full" },
	{ roundTimings: true, systemEvents: true, promptEvents: true, hookExits: "all" },
);

// One projected row before clustering, carrying whether it may still be merged
// into a run and any attachments it emitted alongside itself.
interface Ordered {
	type: "final" | "activity";
	item: MobileTimelineItem;
	pre?: PreActivity;
	attachments?: AttachmentRef[];
}

// One turn's rows, and what outside the turn they depended on.
interface TurnRows {
	entries: Ordered[];
	// A turn's own items decide its rows, with one exception: whether an ask_user
	// call is still answerable is a whole-model question (a later turn's user
	// message answers an earlier ask — deriveAskQuestions.ts). Each entry records
	// the calls this turn consumed and whether they were answerable, so the rows
	// are reused only while that still holds. The rows also depend on the config
	// they were projected under — which items exist as entries at all is the
	// projector's decision at that config — so it is keyed by the config's
	// value (configFingerprint): a caller re-resolving an equal config per
	// publish (resolveEffectiveConfig builds a fresh object every call) still
	// hits the cache, and a config whose value changed re-derives every turn.
	askState: Array<[string, boolean]>;
	configFingerprint: string;
}

// The cluster family one projected row joins. Failed tools and failed unknown
// items never merge (each failed call is its own attention row, exactly the
// pre-re-home family rules); reasoning rows — the live-thought placeholder
// included — merge under one family; completed tools merge under "tool".
function clusterFamilyFor(
	entry: ProjectedEntry,
	row: Extract<MobileTimelineItem, { kind: "activity" }>,
): string {
	if (row.state === "failed" && (row.family === "tool" || row.family === "unknown")) {
		return `failed:${row.id}`;
	}
	if (row.family === "reasoning") return "unknown:reasoning";
	if (row.family === "tool") return "tool";
	return `unknown:${entry.item.type}`;
}

// The attachments emitted alongside an entry's row: the item mapping's own
// rule (itemAttachments) for the kinds that carry them, none for the
// content-free placeholder and the redacted critical failure.
function attachmentsFor(
	entry: ProjectedEntry,
	turnStatus: string | undefined,
): AttachmentRef[] | undefined {
	switch (entry.kind) {
		case "item":
		case "critical":
			if (entry.kind === "critical" && entry.item.type === "reasoning" && entry.redacted) {
				return undefined;
			}
			return itemAttachments(entry.item);
		case "intent":
			// Review round 3 (Low): the attachments ARE output — a settled
			// summarized call drops its images with its text (the same
			// summaryOnly condition intentRow applies), while a failed or
			// still-running call keeps everything per the attention
			// carve-out.
			if (!entry.failed && activityState(entry.item, turnStatus) === "completed") {
				return undefined;
			}
			return itemAttachments(entry.item);
		default:
			return undefined;
	}
}

// Per-turn rows, keyed on the TurnModel reference. The reducer hands a turn back
// UNTOUCHED — by reference — when a frame did not change it (reducer.ts's mapTurn
// and settleFirstMatchingTurn), so a delta into the newest turn leaves every older
// turn's rows exactly as they were. Re-deriving them per frame is the transcript's
// whole width of work — the shared projector's classification scan for the turn
// plus this module's row construction, which still pays a JSON parse per ask and
// two Date.parse calls per timed tool call — for one item's text. A WeakMap so a
// dropped turn's rows go with it. A cache hit skips BOTH halves for that turn:
// the turn is projected alone (see rowsForProjectedTurn), so the projector's
// whole-transcript bookkeeping — anchors, visibleItems, eligibleDisclosureIds —
// is only ever allocated for turns whose rows are actually being re-derived.
// That per-turn slicing is sound because the projector's decisions are
// turn-local (decisionFor reads the item, its turn, and the config); if the
// projector ever went cross-turn, this cache would need a whole-model call.
const turnRowCache = new WeakMap<TurnModel, TurnRows>();

function rowsForProjectedTurn(
	model: ThreadModel,
	turn: TurnModel,
	asks: ReadonlyMap<string, AskQuestionRef[]>,
	config: TranscriptDisplayConfigV1,
	fingerprint: string,
): Ordered[] {
	const cached = turnRowCache.get(turn);
	if (
		cached !== undefined &&
		cached.configFingerprint === fingerprint &&
		cached.askState.every(([callId, answerable]) => asks.has(callId) === answerable)
	) {
		return cached.entries;
	}
	// Project this turn alone. One turn in, one ProjectedTurn out, carrying the
	// same entries a whole-model call yields for this turn (the decisions are
	// turn-local). The slice's entry.sourceIndex restarts at 0 per turn where a
	// whole-model call counts across turns — the row mapping never reads it.
	const [projected] = projectThread({ ...model, turns: [turn] }, config).turns;
	if (projected === undefined) return [];
	const entries: Ordered[] = [];
	const askState: Array<[string, boolean]> = [];
	for (const entry of projected.entries) {
		if (isAskUser(entry.item)) {
			const callId = entry.item.callId ?? entry.item.id;
			askState.push([callId, asks.has(callId)]);
		}
		const row = projectedRow(entry, { turnStatus: turn.status, asks });
		if (row === null) continue;
		// Only an activity row joins a cluster run; everything else is final.
		if (row.kind === "activity") {
			entries.push({
				type: "activity",
				item: row,
				pre: { family: clusterFamilyFor(entry, row), item: row },
				attachments: attachmentsFor(entry, turn.status),
			});
		} else {
			entries.push({
				type: "final",
				item: row,
				attachments: attachmentsFor(entry, turn.status),
			});
		}
	}
	// A turn error produces a failure item at the end of that turn's rows.
	if (turn.error) {
		entries.push({
			type: "final",
			item: failureItem(turn.error as NonNullable<Turn["error"]>, turn.id),
		});
	}
	turnRowCache.set(turn, { entries, askState, configFingerprint: fingerprint });
	return entries;
}

// The attachments row that follows the row which produced it. It points back at
// its source by transcript key, so a page or a reread that reissues the source
// under a new wire id does not orphan its images. An activity's images name the
// source's id when it has no key — a clustered member's row can be rebuilt around
// a different member, and the id is then the only handle left — while any other
// row leaves the field off and lets the reader of the row derive it from the
// row's own id (state/conversation.ts's attachmentSourceId).
function attachmentsRow(
	source: MobileTimelineItem,
	attachments: AttachmentRef[],
	fallbackToId = false,
): { id: string; items: AttachmentRef[]; sourceTranscriptKey?: string } {
	const key = source.transcriptKey ?? (fallbackToId ? source.id : undefined);
	return {
		id: `${source.id}:attachments`,
		items: attachments,
		...(key === undefined ? {} : { sourceTranscriptKey: key }),
	};
}

export function projectTimeline(
	model: ThreadModel,
	// The answerable asks, when the caller has already derived them.
	asks: ReadonlyMap<string, AskQuestionRef[]> = liveAsksFor(model),
	// Which items exist as rows at all is the shared projector's decision at
	// this config; a caller that has none keeps the show-everything default.
	config: TranscriptDisplayConfigV1 = PROJECT_EVERYTHING_CONFIG,
): MobileTimelineItem[] {
	// The shared projector owns visibility and ordering — which entries exist
	// at this config, in turn and item order — and projectedRow maps each
	// surviving entry onto the row the phone renders, carrying whether it is
	// a final item or a clusterable activity pre-item. Attachments emitted
	// alongside an item follow that item in the timeline. Rows already
	// derived for an unchanged turn come from the cache above — and with them
	// that turn's classification scan, which only a cache miss pays (see
	// rowsForProjectedTurn); clustering then runs over the whole result,
	// because a run of activities can span a turn boundary.
	const fingerprint = configFingerprint(config);
	const ordered: Ordered[] = [];
	for (const turn of model.turns) {
		ordered.push(...rowsForProjectedTurn(model, turn, asks, config, fingerprint));
	}

	// Second pass: cluster consecutive activity rows that share a family, then
	// rebuild the timeline in original order.
	const items: MobileTimelineItem[] = [];
	let activityRun: PreActivity[] = [];
	let activityAttachments: Array<{ id: string; items: AttachmentRef[]; sourceTranscriptKey?: string }> = [];

	const flushActivityRun = () => {
		if (activityRun.length === 0) return;
		const clustered = clusterActivityRun(activityRun);
		if (clustered !== undefined) items.push(clustered);
		for (const attachment of activityAttachments) {
			items.push({ kind: "attachments", ...attachment });
		}
		activityRun = [];
		activityAttachments = [];
	};

	for (const entry of ordered) {
		if (entry.type === "activity" && entry.pre) {
			const last = activityRun[activityRun.length - 1];
			if (last && last.family === entry.pre.family) {
				activityRun.push(entry.pre);
			} else {
				flushActivityRun();
				activityRun = [entry.pre];
			}
			if (entry.attachments && entry.attachments.length > 0) {
				activityAttachments.push(attachmentsRow(entry.item, entry.attachments, true));
			}
		} else {
			flushActivityRun();
			items.push(entry.item);
		}
		// Attachments follow the item that produced them.
		if (
			!(entry.type === "activity" && entry.pre) &&
			entry.attachments &&
			entry.attachments.length > 0
		) {
			items.push({ kind: "attachments", ...attachmentsRow(entry.item, entry.attachments) });
		}
	}
	flushActivityRun();

	return items;
}

// The seam's whole-conversation projection: the package model plus the rows
// projected from its turns at the caller's display config.
export function projectConversation(
	model: ThreadModel,
	asks: ReadonlyMap<string, AskQuestionRef[]> = liveAsksFor(model),
	config: TranscriptDisplayConfigV1 = PROJECT_EVERYTHING_CONFIG,
): MobileConversation {
	return { ...model, items: projectTimeline(model, asks, config) };
}

// --- failure rows ----------------------------------------------------------------

function failureItem(
	error: NonNullable<Turn["error"]>,
	turnID: string,
): Extract<MobileTimelineItem, { kind: "failure" }> {
	const title = error.title ?? error.message;
	const parts = [error.message];
	if (error.hint) parts.push(error.hint);
	if (error.additionalDetails) parts.push(error.additionalDetails);
	return {
		kind: "failure",
		// The id names the turn only, never the error's prose: rowsForTurn calls
		// this once per turn (at most one error per turn), so turnID alone is
		// already unique, and the display bound (mobile/src/state/conversation.ts)
		// cuts title/detail but not id — an id built from unbounded prose would
		// stay oversized forever in timelineIdentity, page-ownership sets, list
		// keys and this row's own JSON serialization.
		id: failureRowIdentity(turnID),
		title,
		detail: parts.join("\n"),
	};
}

// The identity a turn's failure row carries — the turn id under the
// failure: prefix. Exported because the store's page-ownership checks read the
// same identity loadOlder recorded for a paginated failure row (the
// failure row is the turn's, not an item's, so this is the one spelling
// both sides must agree on).
export function failureRowIdentity(turnID: string): string {
	return `failure:${turnID}`;
}

// --- row identity -----------------------------------------------------------------
// Which row is which, for every consumer that has to decide whether two rows are
// the same row: the store's page merge and its cap bookkeeping, and any reader
// deduping a reissued row. transcriptKey first, because the hub reissues an item
// under a new wire id while its transcript key stands.

export function attachmentSourceId(item: MobileTimelineItem): string | null {
	return item.kind === "attachments" && item.id.endsWith(":attachments")
		? item.id.slice(0, -":attachments".length)
		: null;
}

export function attachmentSourceIdentity(item: MobileTimelineItem): string | null {
	return item.kind === "attachments"
		? (item.sourceTranscriptKey ?? attachmentSourceId(item))
		: null;
}

export function timelineIdentity(item: MobileTimelineItem): string {
	return item.transcriptKey ?? item.id;
}

// The canonical identity of a clustered activity member — the same
// transcriptKey-first rule timelineIdentity applies to a top-level row.
export function activityIdentity(activity: ActivityMember): string {
	return activity.transcriptKey ?? activity.id;
}

// The identities a row IS: its own, plus every clustered member's. Distinct
// from timelineIdentities, which also carries the identity of the row an
// attachment belongs to — an attachment is not a duplicate of its source.
export function ownTimelineIdentities(item: MobileTimelineItem): Set<string> {
	const identities = new Set([timelineIdentity(item)]);
	if (item.kind === "activity" && item.members) {
		for (const member of item.members) {
			identities.add(activityIdentity(member));
		}
	}
	return identities;
}

export function timelineIdentities(item: MobileTimelineItem): Set<string> {
	const identities = ownTimelineIdentities(item);
	const source = attachmentSourceIdentity(item);
	if (source !== null) identities.add(source);
	return identities;
}

// --- display bounds: limits and truncation helpers (centralized) ------------------
// What a row may cost the reader's device. The store applies these on every
// publish; they live here with the row shape they cut.

export const MAX_ITEM_BYTES = 64 * 1024; // 64 KiB in UTF-8 bytes
export const TRUNCATION_MARKER = "… truncated";
export const RETAINED_ITEM_CAP = 500;

// Truncate a string to maxBytes in UTF-8, ending with "… truncated" exactly
// once whenever the limit is large enough to hold the marker. Iterates
// Unicode scalar values (not UTF-16 code units) so no surrogate pairs are
// split and no U+FFFD replacement chars are produced. The result never
// exceeds maxBytes.
const textEncoder = new TextEncoder();
const markerBytes = textEncoder.encode(TRUNCATION_MARKER);

// UTF-8 byte length without materialising the bytes: every publish asks this of
// every retained row, and all but the oversized ones only need the answer, not
// the encoding. Counting code points is O(length) with no allocation, where
// TextEncoder.encode allocates a byte array as large as the text (and the row that
// is 64 KiB of text allocates it on every publish).
function utf8Length(text: string): number {
	let bytes = 0;
	for (const cp of text) {
		const code = cp.codePointAt(0) ?? 0;
		bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
	}
	return bytes;
}

export function truncateText(text: string, maxBytes: number): string {
	if (utf8Length(text) <= maxBytes) return text;
	// The byte limit is the hard contract: the marker is best effort. Every
	// publish re-applies this to whatever text a row carries, so a limit too
	// small to hold the marker yields the longest prefix that fits, with no
	// marker, rather than a marker that busts the limit.
	const fitsMarker = maxBytes >= markerBytes.length;
	const marker = fitsMarker ? TRUNCATION_MARKER : "";
	const markerLength = fitsMarker ? markerBytes.length : 0;
	const targetBytes = Math.max(0, maxBytes - markerLength);
	// Iterate code points (for...of iterates Unicode scalar values) to find
	// the longest prefix whose UTF-8 encoding fits within targetBytes. This
	// avoids splitting surrogate pairs and never produces U+FFFD.
	let byteLen = 0;
	let cutIdx = 0;
	for (const cp of text) {
		const cpBytes = utf8Length(cp);
		if (byteLen + cpBytes > targetBytes) break;
		byteLen += cpBytes;
		cutIdx += cp.length;
	}
	return text.slice(0, cutIdx) + marker;
}

// One text field, cut to the display bound.
export type BoundText = (text: string) => string;

// Apply the bound to an activity detail's text-bearing fields (description,
// arguments, output, error). Shared by an activity's own top-level detail and
// each of its clustered members' details, so both are bounded the same way. The
// description is the summary line a collapsed row shows
// (mobile-native/src/transcriptPresentation.ts's actionSummary), so it is read
// as much as the output is.
function truncateActivityDetail(detail: ActivityDetail, bound: BoundText): ActivityDetail {
	return {
		...detail,
		description: detail.description ? bound(detail.description) : detail.description,
		arguments: detail.arguments ? bound(detail.arguments) : detail.arguments,
		output: detail.output ? bound(detail.output) : detail.output,
		error: detail.error ? bound(detail.error) : detail.error,
	};
}

// One question's prose and option text, bounded like every other row's:
// header, question, why, ifUnanswered, and every option's label and detail
// (AskUserOption.detail is a required string, never undefined). Used only by
// truncateItem's "question" case below: the DISPLAY rows a reader scrolls
// carry these cut copies. Nothing that answers a question reads them —
// mobile-native's pendingQuestions (questionAnswers.ts) asks the model for
// the canonical refs through liveAsksFor, the same call these rows were
// projected from — so a submitted answer always names exactly the label the
// agent offered, never a cut remnant, and two options sharing a prefix
// longer than the bound stay distinguishable to the answer composer.
export function boundQuestion(
	question: AskQuestionRef,
	bound: BoundText,
): AskQuestionRef {
	return {
		...question,
		header: bound(question.header),
		question: bound(question.question),
		...(question.why === undefined ? {} : { why: bound(question.why) }),
		...(question.ifUnanswered === undefined
			? {}
			: { ifUnanswered: bound(question.ifUnanswered) }),
		options: question.options.map((option) => ({
			...option,
			label: bound(option.label),
			detail: bound(option.detail),
		})),
	};
}

// Apply the bound to every text a row carries for the reader. Native transcript
// projection expands a clustered activity's members directly, so each member's
// own detail is bounded too — not just the cluster's top-level detail (the first
// member's). A pasted user message, a daemon notice and a tool failure's stack
// are as large as anything that streams, so each kind that carries prose is here.
export function truncateItem(item: MobileTimelineItem, bound: BoundText): MobileTimelineItem {
	switch (item.kind) {
		case "user":
			return { ...item, text: bound(item.text) };
		case "assistant":
			return { ...item, markdown: bound(item.markdown) };
		case "notice":
			return { ...item, text: bound(item.text) };
		case "failure":
			return { ...item, title: bound(item.title), detail: bound(item.detail) };
		case "question":
			return {
				...item,
				questions: item.questions.map((question) => boundQuestion(question, bound)),
			};
		case "activity":
			// The label is rendered twice on the phone — the disclosure line and its
			// accessibility label (mobile-native/src/TimelineItem.tsx) — so it is
			// bounded like the detail it heads, for the row and for every member.
			return {
				...item,
				label: bound(item.label),
				detail: truncateActivityDetail(item.detail, bound),
				...(item.members
					? {
							members: item.members.map((member) => ({
								...member,
								label: bound(member.label),
								detail: truncateActivityDetail(member.detail, bound),
							})),
						}
					: {}),
			};
		case "attachments":
			// Only the display name is bounded — it is plain display text the
			// renderer inserts into accessibility labels and modal copy. src is a
			// data URI or a resolved fetch URL, and cutting it yields something the
			// renderer cannot decode, so it passes through verbatim.
			return {
				...item,
				items: item.items.map((attachment) => ({
					...attachment,
					name: attachment.name ? bound(attachment.name) : attachment.name,
				})),
			};
		default:
			// A forward-compatible row kind: nothing here knows its fields, so it
			// passes through untouched rather than guessed at.
			return item;
	}
}

// Enforce the 500-item retained cap. Always retains the NEWEST items (end
// of array) so the live tail is preserved for interactive scrolling.
//
// The projector always emits an attachment immediately after the row it
// belongs to, so a source/attachment pair can only ever straddle the cut at
// the front of the slice — an attachment kept while its source, one slot
// earlier, was not. Dropping the source and leaving the attachment is worse
// than dropping both: the orphan's lingering identity (timelineIdentities
// exposes an attachment's source alongside its own) makes loadOlder treat a
// genuine older-page copy of that source as an already-seen duplicate
// (conversation.ts's F10 admission rule), permanently refusing to recover
// it. Drop every leading attachment whose source did not survive the cut.
export function capItems(items: MobileTimelineItem[]): MobileTimelineItem[] {
	if (items.length <= RETAINED_ITEM_CAP) return items;
	const sliced = items.slice(items.length - RETAINED_ITEM_CAP);
	// The identity set is only ever consulted for a leading run of orphaned
	// attachments (the loop below breaks the moment sliced[orphaned] is not an
	// attachment) — skip building it over all 500 rows on the common publish
	// where the cut did not land on an attachment at all.
	if (attachmentSourceIdentity(sliced[0]) === null) return sliced;
	const keptIds = new Set(sliced.flatMap((item) => [...ownTimelineIdentities(item)]));
	let orphaned = 0;
	while (orphaned < sliced.length) {
		const sourceId = attachmentSourceIdentity(sliced[orphaned]);
		if (sourceId === null || keptIds.has(sourceId)) break;
		orphaned++;
	}
	return orphaned === 0 ? sliced : sliced.slice(orphaned);
}
