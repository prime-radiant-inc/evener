// The native recovery surface's view of one target's durable recovery rows.
// It is the panel half of the landed slice-4 hook: the hook owns the read,
// the storage subscription and the one recovery write; this module is the
// pure projection the screen turns into ghosts at the transcript's end
// (session/ghosts.ts), the line that shows the surface's own failure, and the
// consumer hook that owns the screen's recovery state.
//
// Two recovery offers, and only these two:
// - restore: writes a rejected row's recovered text into the composer through
//   the draft repository's own savepointed write (DraftDocument
//   .restoreRecoveredDraft), so a failed restore can never half-write a draft.
// - discard: retires one durable row through the hook's discardRecovery, which
//   is scoped to the row's clientMutationId AND the projection's composite
//   target key. `discardRecoveredMutation` makes that scoping explicit: a row
//   another target owns is refused here, so the surface can never issue a
//   blanket clear.

import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
	MutationRecoveryKind,
	MutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";
import { useCallback, useEffect, useRef, useState } from "react";
import { View } from "react-native";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { type NativeMutationRecoveryRuntime, useNativeMutationRecovery } from "./useNativeMutationRecovery";
import { ErrorMessage } from "./ui";
import { Button } from "./sheet/Grouped";

export type NativeMutationRecoveryStatus = MutationRecoveryKind;
export type NativeMutationRecoveryAction = "restore" | "discard";

export interface NativeMutationRecoveryRow {
	targetKey: string;
	clientMutationId: string;
	intentSequence: number;
	status: NativeMutationRecoveryStatus;
	reason?: string;
	/** The text a restore writes into the composer. */
	text: string;
	/** The queued messages a refused steer from the queue was for. It sends
	 * no text of its own, so there is nothing to restore. */
	queuedText?: string;
	/** Whether the record carries image inputs a composer restore cannot
	 * reconstitute here. Restore is withheld for such a row rather than
	 * silently dropping the image. */
	carriesAttachments: boolean;
	/** Whether the record offers Restore on its own: a rejected, non-interrupt
	 * record whose text a restore can write and which carries no attachment a
	 * text-only restore would drop. The projection derives this from the record
	 * alone, and `actions` carries Restore exactly when it holds, so the two
	 * always agree. Whether the composer can accept a restore right now is the
	 * ghost's `canEdit` at render time, not a projection input. */
	restoreOffered: boolean;
	record: MutationRecoveryRecord<MutationAttachmentRef>;
	actions: readonly NativeMutationRecoveryAction[];
}

function isTextInputItem(item: unknown): item is { type: "text"; text: string } {
	return (
		typeof item === "object" &&
		item !== null &&
		(item as { type?: unknown }).type === "text" &&
		typeof (item as { text?: unknown }).text === "string"
	);
}

// Any image input counts, whatever fields carry its bytes: a `{type:"image"}`
// item whose data lives under `path` or metadata only must still withhold
// restore, or a text-only restore would silently drop the image.
function isImageInputItem(item: unknown): item is { type: "image" } {
	return typeof item === "object" && item !== null && (item as { type?: unknown }).type === "image";
}

// Whether the rejected mutation carries attachments a text-only restore would
// drop. Two spells of the same thing: native intents persist the composer's
// `{type:"image", data}` items in the payload, and the shared record shape also
// carries its attachment metadata on `record.attachments`. A row is
// non-restorable if either is present, so a text-only restore never silently
// loses an image.
export function recordCarriesAttachments(record: MutationRecoveryRecord<MutationAttachmentRef>): boolean {
	const input = record.payload.input;
	return (record.attachments?.length ?? 0) > 0 || (Array.isArray(input) && input.some(isImageInputItem));
}

// The text a rejected mutation restores to the composer. `composerText` is the
// composer's own text with "[image N]" anchors intact; a record written before
// that field existed falls back to the payload's text items - the same
// fallback the web recovery draft takes, so no recoverable text is lost.
export function recoveredComposerText(record: MutationRecoveryRecord<MutationAttachmentRef>): string {
	if (typeof record.composerText === "string" && record.composerText.length > 0) return record.composerText;
	return inputText(record.payload.input);
}

// The text items of an input list, one per line; "" for anything else.
function inputText(input: unknown): string {
	if (!Array.isArray(input)) return "";
	return input
		.filter(isTextInputItem)
		.map((item) => item.text)
		.join("\n");
}

// A promote or drain from the queue steers with messages the hub holds; its
// ghost shows them from the record's display.
function queuedSteerText(record: MutationRecoveryRecord<MutationAttachmentRef>): string | undefined {
	if (record.method !== "turn/promoteQueuedAsSteer" && record.method !== "turn/drainAsSteer") return undefined;
	return inputText((record.optimisticDisplay as { input?: unknown } | null)?.input);
}

// Whether a record can offer a restore at all, independent of the composer: a
// rejected, non-interrupt mutation whose text a restore can write and which
// carries no attachment a text-only restore would drop. A Stop's interrupt is
// never restorable - it carries no composer text to replay - so the fence is
// explicit rather than inferred from the text alone. This is the record-aware
// half of the fence, and now the whole of it: whether the composer can accept a
// restore right now is the ghost's `canEdit` at render time, not a projection
// input.
export function recordOffersRestore(record: MutationRecoveryRecord<MutationAttachmentRef>): boolean {
	return (
		record.recoveryKind === "rejected" &&
		record.method !== "turn/interrupt" &&
		recoveredComposerText(record).length > 0 &&
		!recordCarriesAttachments(record)
	);
}

// The action derivation. Restore follows the record-aware fence alone: neither
// the fence nor its absence has a default, so a caller cannot reach Restore by
// omission. The discard offer is always present, so a row can never be stuck
// with no way out.
export function nativeMutationRecoveryActions(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): readonly NativeMutationRecoveryAction[] {
	if (recordOffersRestore(record)) return ["restore", "discard"];
	return ["discard"];
}

// The recovery records one exact target owns; the projection layers each
// record's actions on top.
function targetRecoveryRecords(
	targetKey: string,
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null,
): MutationRecoveryRecord<MutationAttachmentRef>[] {
	if (snapshot === null) return [];
	return snapshot.recovery.filter((record) => record.targetRef === targetKey);
}

export function projectNativeMutationRecovery(
	targetKey: string,
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null,
): NativeMutationRecoveryRow[] {
	const rows: NativeMutationRecoveryRow[] = [];
	for (const record of targetRecoveryRecords(targetKey, snapshot)) {
		const text = recoveredComposerText(record);
		const carriesAttachments = recordCarriesAttachments(record);
		const restoreOffered = recordOffersRestore(record);
		const queuedText = text === "" ? queuedSteerText(record) : undefined;
		rows.push({
			targetKey,
			clientMutationId: record.clientMutationId,
			intentSequence: record.intentSequence,
			status: record.recoveryKind,
			...(record.recoveryReason === undefined ? {} : { reason: record.recoveryReason }),
			text,
			...(queuedText === undefined ? {} : { queuedText }),
			carriesAttachments,
			restoreOffered,
			record,
			actions: nativeMutationRecoveryActions(record),
		});
	}
	return rows.sort(
		(left, right) =>
			left.intentSequence - right.intentSequence || left.clientMutationId.localeCompare(right.clientMutationId),
	);
}

// The exact-target discard: the row's clientMutationId is discarded through the
// projection's own runtime, whose target key is the only one it can reach. A
// row a different target owns is refused before the runtime is touched, so this
// is never a blanket clear of a target's recovery rows.
export function discardRecoveredMutation(
	projection: {
		targetKey: string;
		discard(clientMutationId: string): Promise<boolean>;
	},
	row: NativeMutationRecoveryRow,
): Promise<boolean> {
	if (row.targetKey !== projection.targetKey) return Promise.resolve(false);
	return projection.discard(row.clientMutationId);
}

export function recoveryFailureMessage(error: unknown): string {
	if (error instanceof Error) return error.message;
	if (typeof error === "string" && error.length > 0) return error;
	return "Recovery is unavailable.";
}

export interface RecoveryPanelSurface {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	/** A read, acquisition or discard failure: the projection's own read error,
	 * or the acquisition/discard failure the read projection cannot carry. */
	error: unknown;
	failed: boolean;
	retry(): void;
	discard(row: NativeMutationRecoveryRow): void;
}

// Owns the screen's recovery state: it acquires the runtime lazily (only once
// connected, so a screen that never reaches a live conversation never opens the
// mutations database), follows the landed hook's projection, and turns the two
// failure paths the runtime deliberately propagates - a failed acquisition and
// a rejected discard - into visible state a Retry can clear.
export function useRecoveryPanel({
	connected,
	hubId,
	targetRef,
	acquire = getNativeMutationRuntime,
}: {
	connected: boolean;
	hubId: string;
	targetRef: string;
	acquire?: () => NativeMutationRecoveryRuntime;
}): RecoveryPanelSurface {
	const targetKey = nativeMutationTargetKey(hubId, targetRef);
	const [runtime, setRuntime] = useState<NativeMutationRecoveryRuntime | null>(null);
	// The surface's own failure is scoped to the target (and the retry attempt):
	// a discard that rejects after the screen moved to another target must not
	// hide the new target's rows. `scope` is derived, so it changes with the
	// target and with a retry, and a failure is exposed only while its scope is
	// still current.
	const [failure, setFailure] = useState<{
		scope: string;
		error: unknown;
	} | null>(null);
	const [attempt, setAttempt] = useState(0);
	// Serializes discards so only the newest completion publishes its outcome.
	const discardGeneration = useRef(0);
	const scope = `${targetKey}::${attempt}`;

	useEffect(() => {
		if (!connected || runtime !== null) return;
		try {
			setRuntime(acquire());
			// A successful acquisition supersedes any earlier failure, whatever
			// scope it was recorded under: leaving a target-scoped failure in place
			// would resurface it if the screen later returned to that target, over
			// an otherwise-healthy read.
			setFailure(null);
		} catch (error) {
			setFailure({ scope, error });
		}
	}, [connected, runtime, acquire, scope]);

	const projection = useNativeMutationRecovery(runtime, targetKey);
	// Retry re-acquires: the landed hook re-reads whenever its runtime identity
	// changes, so dropping to null and re-acquiring refreshes a failed read
	// without the landed hook growing a refresh surface.
	const retry = useCallback(() => {
		setFailure(null);
		setRuntime(null);
		setAttempt((value) => value + 1);
	}, []);
	const discard = useCallback(
		(row: NativeMutationRecoveryRow) => {
			// Only the newest discard may publish its outcome: a stale rejection
			// from an earlier discard (or an earlier target) must not overwrite a
			// newer operation's state or resurrect an old error.
			const generation = ++discardGeneration.current;
			void discardRecoveredMutation(projection, row).then(
				(discarded) => {
					if (generation !== discardGeneration.current) return;
					// A refused no-op (a foreign row) is not a success to act on.
					if (discarded) setFailure((current) => (current?.scope === scope ? null : current));
				},
				(error) => {
					if (generation !== discardGeneration.current) return;
					setFailure({ scope, error });
				},
			);
		},
		[projection, scope],
	);

	const localFailure = failure !== null && failure.scope === scope ? failure.error : null;
	const error = localFailure ?? projection.error;
	return {
		targetKey,
		snapshot: projection.snapshot,
		error,
		failed: error !== null && error !== undefined,
		retry,
		discard,
	};
}

/** The recovery surface's own failure: a read, acquisition or discard that
 * failed, with Retry. It sits above the ghosts, never in place of them, so a
 * failed discard can't hide rows that can still be recovered. */
export function RecoveryFailure({ error, onRetry }: { error: unknown; onRetry: () => void }) {
	return (
		<View style={{ alignItems: "flex-start" }}>
			<ErrorMessage message={recoveryFailureMessage(error)} />
			<Button text quiet label="Retry" onPress={onRetry} />
		</View>
	);
}
