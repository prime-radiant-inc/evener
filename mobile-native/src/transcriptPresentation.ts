import {
	contentVectorForConfig,
	sessionTokens,
	tokenUnitLabel,
	type EvenerUsage,
	type SessionTokens,
	type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { ActivityMember, MobileConversation, MobileTimelineItem } from "./projectedRows";
import { isStep } from "./session/transcriptRows";

export type ActivityPresentation = {
	mode: "full" | "intent" | "critical";
	summary?: string;
};

// The cumulative breakdown the wire reports beside the derived pair: the
// thread's own cache-read and total figures. EvenerThread.Usage is not
// windowed the way turns are, so both are whole-session and carry no scope of
// their own.
type CumulativeTokens = Pick<EvenerUsage, "cacheReadTokens" | "totalTokens">;

// The session accounting the transcript footer shows: the conversation's
// token total (the package's turn-summed sessionTokens derivation, shared
// with the web details panel, carrying its own scope), the thread's own
// cumulative cache/total breakdown, and cost - each null when the display
// config hides it or the daemon reported none.
//
// The derived pair and its scope stay together in `derived`, never flattened
// into `cumulative`: the wire's EvenerUsage permits a sparse cumulative
// object (cache or total alone, with no input/output pair at all), and
// sessionTokens has no per-turn equivalent for them, so they must survive
// even when sessionTokens falls back to summing turns or returns null
// outright. cacheReadTokens/totalTokens are always whole-session figures, so
// they must never inherit whatever scope the derived pair got.
export interface SessionAccounting {
	derived: SessionTokens | null;
	cumulative: CumulativeTokens | null;
	cost: string | null;
}

export interface UsageRow {
	label: "Input" | "Output" | "Cached" | "Total";
	value: number;
	unit: string;
}

// usageRows picks the footer's visible rows and labels each with what it
// actually counts. Input/Output take the derived pair's own scope (a
// truncated turn window says so); Cached/Total are always the thread's whole
// -session cumulative figures, so they are labelled with the explicit session
// scope, never whatever scope the derived pair got.
export function usageRows(accounting: Pick<SessionAccounting, "derived" | "cumulative"> | null): UsageRow[] {
	if (!accounting) return [];
	const { derived, cumulative } = accounting;
	const derivedUnit = tokenUnitLabel(derived?.scope);
	const cumulativeUnit = tokenUnitLabel("session");
	const rows: UsageRow[] = [];
	const add = (label: UsageRow["label"], value: number | undefined, unit: string): void => {
		if (value !== undefined) rows.push({ label, value, unit });
	};
	add("Input", derived?.inputTokens, derivedUnit);
	add("Output", derived?.outputTokens, derivedUnit);
	add("Cached", cumulative?.cacheReadTokens, cumulativeUnit);
	add("Total", cumulative?.totalTokens, cumulativeUnit);
	return rows;
}

export interface NativeTranscriptPresentation {
	items: MobileTimelineItem[];
	activityPresentation: ReadonlyMap<string, ActivityPresentation>;
	expandByDefault: boolean;
	/** The live run shows its steps: at the levels that show tool calls. At
	 * Chat and Intent the tray shows the live step, so the run keeps to its
	 * line (S7). */
	liveRunsOpen: boolean;
	usage: SessionAccounting | null;
	showDuration: boolean;
}

const ACTION_SUMMARY_UNAVAILABLE = "Action summary unavailable";

function actionSummary(item: Extract<MobileTimelineItem, { kind: "activity" }>): string {
	// The step's own words when it has no rationale: the one path to them
	// (projectedRows builds them with the package's toolStepSummary).
	return item.detail.description?.trim() || item.detail.summary || ACTION_SUMMARY_UNAVAILABLE;
}

// An activity that is running or failed is attention-worthy. Notice criticality
// is timeline.ts's isCriticalNotice, not a rule of this layer.
function activityIsCritical(item: Extract<MobileTimelineItem, { kind: "activity" }>): boolean {
	return item.state === "failed" || item.state === "running";
}

// How an activity row renders. This is a RENDERING hint only: which rows
// exist at all is the shared projector's decision at the user's display
// config, made once inside the store's seam (D24-6 retired this layer's own
// config-driven row filtering — the projector subsumed it). What remains
// here is the mode each surviving row renders in:
//   - a running or failed activity renders as attention: its summary line
//     above an expandable body (tools carry the summary; reasoning rows do
//     not — their body is the thought) — the attention rule outranks the
//     summarization, so a failed call never collapses to a bare line;
//   - a summary-only row (the projector's intent entry; the operator's
//     summary-only ruling) renders its summary line, nothing to expand;
//   - everything else renders in full.
function presentationFor(item: Extract<MobileTimelineItem, { kind: "activity" }>): ActivityPresentation {
	if (activityIsCritical(item)) {
		return {
			mode: "critical",
			...(item.family === "tool"
				? {
						summary: actionSummary(item),
					}
				: {}),
		};
	}
	if (item.summaryOnly === true) {
		return { mode: "intent", summary: actionSummary(item) };
	}
	return FULL_PRESENTATION;
}

// Without transcript preferences nothing is summarised: every activity shows
// in full until the hub's config arrives, or forever on a hub that does not
// support it.
// One object stands under every activity id in that map, so it is readonly:
// nothing may edit one row's presentation and move the rest with it.
const FULL_PRESENTATION = { mode: "full" } as const;

function memberItem(member: ActivityMember): Extract<MobileTimelineItem, { kind: "activity" }> {
	return {
		kind: "activity",
		id: member.id,
		label: member.label,
		family: member.family,
		state: member.state,
		detail: member.detail,
		...(member.summaryOnly ? { summaryOnly: member.summaryOnly } : {}),
		...(member.transcriptKey ? { transcriptKey: member.transcriptKey } : {}),
		...(member.position ? { position: member.position } : {}),
		...(member.turnId ? { turnId: member.turnId } : {}),
	};
}

// A cumulative field's Go zero value ("0") signals absence, not a real
// measurement of zero — the same rule sessionTokens applies to inputTokens/
// outputTokens (threadUsage.ts). cacheReadTokens/totalTokens get no such
// derivation of their own (they are read straight off the wire), so that
// rule is applied here, once, at the point they are read.
function noZero(value: number | undefined): number | undefined {
	return value === 0 ? undefined : value;
}

function accountingFor(
	conversation: MobileConversation | null,
	config: TranscriptDisplayConfigV1,
): SessionAccounting | null {
	if (!conversation) return null;
	const cost = config.advanced.estimatedCost ? (conversation.cost ?? null) : null;
	if (!config.advanced.tokenCounts) return { derived: null, cumulative: null, cost };
	const derived = sessionTokens(conversation);
	const cacheReadTokens = noZero(conversation.usage?.cacheReadTokens);
	const totalTokens = noZero(conversation.usage?.totalTokens);
	const cumulative =
		cacheReadTokens !== undefined || totalTokens !== undefined ? { cacheReadTokens, totalTokens } : null;
	return { derived, cumulative, cost };
}

// Chat is just the conversation and its subagents (spec 8.2; the transcript
// rows rulings, 2026-09-29). A settled step goes, and so does a running one,
// which the tray already shows. A failed step stays, with its images, since a
// failure is something the reader should see at every level. A subagent and a
// question are rows of their own, never steps, so they stay. No dropped step
// leaves images behind: a settled step at Intent is summary-only, which drops
// them, and the hub attaches a step's images only when it settles.
//
// A daemon steer is instructions to the agent, never the conversation, so its
// notice goes too, the notification cards a steer carries included: the
// subagent's own row, which Chat keeps, already says its state. What stays is
// the conversation's own: a steered-in message is the human's words (a user
// row), and a saved note is spec 8.8's every-level row (a note row) — neither
// is a steering notice (Jesse, 2026-10-03).
function conversationOnly(items: MobileTimelineItem[]): MobileTimelineItem[] {
	return items.filter((item) => !isDroppedStep(item) && !isDaemonSteering(item));
}

// A settled or running step; a failed one is not dropped (see above).
function isDroppedStep(item: MobileTimelineItem): boolean {
	return isStep(item) && item.state !== "failed";
}

function isDaemonSteering(item: MobileTimelineItem): boolean {
	return item.kind === "notice" && item.origin === "steering";
}

export function projectNativeTranscript(
	conversation: MobileConversation | null,
	config: TranscriptDisplayConfigV1 | null | undefined,
	{ justTheConversation = false }: { justTheConversation?: boolean } = {},
): NativeTranscriptPresentation {
	const source = conversation?.items ?? [];
	const projected = projectTimeline(source, config);
	const { activityPresentation } = projected;
	const items = justTheConversation ? conversationOnly(projected.items) : projected.items;
	if (!config)
		return {
			items,
			activityPresentation,
			expandByDefault: false,
			liveRunsOpen: false,
			usage: null,
			showDuration: true,
		};
	const content = contentVectorForConfig(config);
	return {
		items,
		activityPresentation,
		expandByDefault: content.expandByDefault,
		liveRunsOpen: content.toolCalls,
		usage: accountingFor(conversation, config),
		showDuration: config.advanced.roundTimings,
	};
}

// The one place that decides where a row sits relative to its attachments:
// every activity is followed by the attachments that name it as their source,
// and the trailing rows those came from are dropped. Both the configured path
// and the no-config fallback emit through this, so the two cannot disagree
// about adjacency; without a config every activity is simply shown in full.
//
// Every row the seam projected reaches the renderer: the shared projector
// decided at the user's config which rows exist (D24-6), so this pass only
// reshapes what survived — unrolling clustered members and seating
// attachments beside the member that produced them — and computes each
// activity's rendering mode.
function projectTimeline(
	source: MobileTimelineItem[],
	config: TranscriptDisplayConfigV1 | null | undefined,
): {
	items: MobileTimelineItem[];
	activityPresentation: Map<string, ActivityPresentation>;
} {
	const activityPresentation = new Map<string, ActivityPresentation>();
	const membersByKey = new Set<string>();
	for (const item of source)
		if (item.kind === "activity")
			for (const member of item.members ?? []) membersByKey.add(member.transcriptKey ?? member.id);
	const attachmentsByKey = new Map<string, MobileTimelineItem[]>();
	for (const item of source) {
		if (item.kind !== "attachments" || !item.sourceTranscriptKey || !membersByKey.has(item.sourceTranscriptKey))
			continue;
		const attachments = attachmentsByKey.get(item.sourceTranscriptKey) ?? [];
		attachments.push(item);
		attachmentsByKey.set(item.sourceTranscriptKey, attachments);
	}
	const projectedItems: MobileTimelineItem[] = [];
	for (const item of source) {
		if (item.kind === "activity" && item.members?.length) {
			for (const member of item.members) {
				const projected = memberItem(member);
				activityPresentation.set(projected.id, config ? presentationFor(projected) : FULL_PRESENTATION);
				projectedItems.push(projected);
				// Attachments keep their source position even when that activity is hidden.
				projectedItems.push(...(attachmentsByKey.get(member.transcriptKey ?? member.id) ?? []));
			}
		} else if (item.kind === "activity") {
			activityPresentation.set(item.id, config ? presentationFor(item) : FULL_PRESENTATION);
			projectedItems.push(item);
		} else if (
			item.kind === "attachments" &&
			item.sourceTranscriptKey &&
			attachmentsByKey.has(item.sourceTranscriptKey)
		) {
		} else {
			projectedItems.push(item);
		}
	}
	return { items: projectedItems, activityPresentation };
}
