// The native recovery surface's view of one target's durable recovery rows.
// It is the panel half of the landed slice-4 hook: the hook owns the read,
// the storage subscription and the one recovery write; this module is the
// pure projection, the presentational list a screen mounts inside its
// recovery modal, and the consumer hook that owns the screen's recovery state.
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
import {
	type NativeMutationRecoveryRuntime,
	useNativeMutationRecovery,
} from "./useNativeMutationRecovery";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export type NativeMutationRecoveryStatus = MutationRecoveryKind;
export type NativeMutationRecoveryAction = "restore" | "discard";

export interface NativeMutationRecoveryRow {
	targetKey: string;
	clientMutationId: string;
	intentSequence: number;
	status: NativeMutationRecoveryStatus;
	label: string;
	reason?: string;
	/** The text a restore writes into the composer. */
	text: string;
	/** Whether the record carries image inputs a composer restore cannot
	 * reconstitute here. Restore is withheld for such a row rather than
	 * silently dropping the image. */
	carriesAttachments: boolean;
	record: MutationRecoveryRecord<MutationAttachmentRef>;
	actions: readonly NativeMutationRecoveryAction[];
}

const labels = {
	rejected: "Rejected",
	orphaned: "Needs review",
} satisfies Record<NativeMutationRecoveryStatus, string>;

function isTextInputItem(
	item: unknown,
): item is { type: "text"; text: string } {
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
	return (
		typeof item === "object" &&
		item !== null &&
		(item as { type?: unknown }).type === "image"
	);
}

// Whether the rejected mutation carries attachments a text-only restore would
// drop. Two spells of the same thing: native intents persist the composer's
// `{type:"image", data}` items in the payload, and the shared record shape also
// carries its attachment metadata on `record.attachments`. A row is
// non-restorable if either is present, so a text-only restore never silently
// loses an image.
export function recordCarriesAttachments(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): boolean {
	const input = record.payload.input;
	return (
		(record.attachments?.length ?? 0) > 0 ||
		(Array.isArray(input) && input.some(isImageInputItem))
	);
}

// The text a rejected mutation restores to the composer. `composerText` is the
// composer's own text with "[image N]" anchors intact; a record written before
// that field existed falls back to the payload's text items - the same
// fallback the web recovery draft takes, so no recoverable text is lost.
export function recoveredComposerText(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): string {
	if (typeof record.composerText === "string" && record.composerText.length > 0)
		return record.composerText;
	const input = record.payload.input;
	if (!Array.isArray(input)) return "";
	return input.filter(isTextInputItem).map((item) => item.text).join("\n");
}

// Whether a record can offer a restore at all, independent of the composer: a
// rejected mutation whose text a restore can write and which carries no
// attachment a text-only restore would drop. This is the record-aware half of
// the fence; the screen owns the other half - whether the composer can accept a
// restore right now - and passes its result in, so no converter logic lives
// here.
export function recordOffersRestore(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): boolean {
	return (
		record.recoveryKind === "rejected" &&
		recoveredComposerText(record).length > 0 &&
		!recordCarriesAttachments(record)
	);
}

// The action derivation. Restore cannot be obtained without BOTH an explicit
// record that passes the record-aware fence AND an explicit converter result:
// neither has a default, so a caller cannot reach Restore by omission. The
// discard offer is always present, so a row can never be stuck with no way out.
export function nativeMutationRecoveryActions(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
	canRestore: boolean,
): readonly NativeMutationRecoveryAction[] {
	if (recordOffersRestore(record) && canRestore) return ["restore", "discard"];
	return ["discard"];
}

// The recovery records one exact target owns; the projection layers each
// record's actions on top. A count that needs only which rows exist uses this
// directly, since the converter result cannot change the number of rows.
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
	canRestore: (record: MutationRecoveryRecord<MutationAttachmentRef>) => boolean,
): NativeMutationRecoveryRow[] {
	const rows: NativeMutationRecoveryRow[] = [];
	for (const record of targetRecoveryRecords(targetKey, snapshot)) {
		const text = recoveredComposerText(record);
		const carriesAttachments = recordCarriesAttachments(record);
		rows.push({
			targetKey,
			clientMutationId: record.clientMutationId,
			intentSequence: record.intentSequence,
			status: record.recoveryKind,
			label: labels[record.recoveryKind],
			...(record.recoveryReason === undefined
				? {}
				: { reason: record.recoveryReason }),
			text,
			carriesAttachments,
			record,
			actions: nativeMutationRecoveryActions(record, canRestore(record)),
		});
	}
	return rows.sort(
		(left, right) =>
			left.intentSequence - right.intentSequence ||
			left.clientMutationId.localeCompare(right.clientMutationId),
	);
}

export interface MutationRecoveryActions {
	/** Whether the composer can accept a restore of this record right now:
	 * recovery must not silently do nothing when an existing draft or image
	 * would be clobbered. */
	canRestore(record: MutationRecoveryRecord<MutationAttachmentRef>): boolean;
	/** Why a restore is currently blocked, for an accurate disabled-restore
	 * hint; falls back to a generic sentence when the caller supplies none. */
	restoreHint?(row: NativeMutationRecoveryRow): string | null;
	onRestore(row: NativeMutationRecoveryRow): void;
	onDiscard(row: NativeMutationRecoveryRow): void;
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

// The recovery entry point. Normally it is strictly row-conditional: no rows
// means no entry, so no dead control ships that opens an empty recovery surface
// (durable submission, which makes rows exist, is a later slice; until then the
// entry stays hidden and activates with no further UI change). The one
// exception is a failed recovery surface: with no rows there is no other way
// into the modal, so a failure renders the entry and opening it exposes the
// panel's error and Retry rather than hiding them behind an unopenable modal.
export function shouldOfferRecoveryEntry({
	connected,
	deliveryConcern,
	count,
	failed,
}: {
	connected: boolean;
	deliveryConcern: boolean;
	count: number;
	failed: boolean;
}): boolean {
	return connected && !deliveryConcern && (count > 0 || failed);
}

export interface RecoveryPanelSurface {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	loading: boolean;
	/** A read, acquisition or discard failure: the projection's own read error,
	 * or the acquisition/discard failure the read projection cannot carry. */
	error: unknown;
	failed: boolean;
	count: number;
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
	const [runtime, setRuntime] = useState<NativeMutationRecoveryRuntime | null>(
		null,
	);
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
					if (discarded)
						setFailure((current) => (current?.scope === scope ? null : current));
				},
				(error) => {
					if (generation !== discardGeneration.current) return;
					setFailure({ scope, error });
				},
			);
		},
		[projection, scope],
	);

	const localFailure =
		failure !== null && failure.scope === scope ? failure.error : null;
	const error = localFailure ?? projection.error;
	return {
		targetKey,
		snapshot: projection.snapshot,
		// A connected surface with no runtime yet is still loading: its read is
		// pending acquisition. Only a disconnected/never-acquired surface is
		// genuinely unavailable, which is what lets the panel tell the two apart.
		loading: projection.loading || (connected && runtime === null),
		error,
		failed: error !== null && error !== undefined,
		count: targetRecoveryRecords(targetKey, projection.snapshot).length,
		retry,
		discard,
	};
}

export function MutationRecoveryPanel({
	targetKey,
	snapshot,
	error,
	onRetry,
	loading = false,
	actions,
}: {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	error: unknown;
	onRetry?: () => void;
	loading?: boolean;
	actions: MutationRecoveryActions;
}) {
	const colors = useColors();
	// A failure renders as a banner above whatever rows exist, never as a
	// replacement for them: a failed discard must not hide a target's still
	// recoverable rows behind its own error.
	const failure =
		error !== null && error !== undefined ? (
			<View style={{ gap: 8 }}>
				<ErrorMessage message={recoveryFailureMessage(error)} />
				{onRetry ? (
					<Action tone="quiet" onPress={onRetry}>
						Retry
					</Action>
				) : null}
			</View>
		) : null;
	if (snapshot === null) {
		if (failure) return failure;
		if (loading) return <Copy muted>Loading delivery status…</Copy>;
		return <Copy muted>Recovery is unavailable.</Copy>;
	}
	const rows = projectNativeMutationRecovery(
		targetKey,
		snapshot,
		(record) => actions.canRestore(record),
	);
	return (
		<View style={{ gap: 12 }}>
			{failure}
			{rows.length === 0 ? (
				<Copy muted>No messages need recovery.</Copy>
			) : null}
			{rows.map((row) => {
				// The record-aware fence decides whether Restore is offered at all;
				// the converter result already folded into row.actions decides
				// whether it is actionable now. A record-eligible row the composer
				// cannot accept still shows a disabled Restore with the caller's hint.
				const offersRestore = recordOffersRestore(row.record);
				const restorable = row.actions.includes("restore");
				return (
					<View
						key={row.clientMutationId}
						style={[styles.card, { borderColor: colors.border }]}
					>
						<Copy>{row.label}</Copy>
						{row.reason ? <Copy muted>{row.reason}</Copy> : null}
						{row.text ? (
							<Copy muted numberOfLines={3}>
								{row.text}
							</Copy>
						) : null}
						<View style={styles.row}>
								{offersRestore ? (
								<Action
										disabled={!restorable}
									onPress={() => {
											if (restorable) actions.onRestore(row);
									}}
								>
										Restore to draft
								</Action>
								) : null}
								<Action onPress={() => actions.onDiscard(row)}>Discard</Action>
						</View>
						{offersRestore && !restorable ? (
							<Copy muted>
								{actions.restoreHint?.(row) ??
									"This message can't be restored to the draft right now."}
							</Copy>
						) : null}
						{row.status === "rejected" &&
						!offersRestore &&
						row.carriesAttachments ? (
							<Copy muted>
								This message carried an image, so it can't be restored to the
								draft here.
							</Copy>
						) : null}
					</View>
				);
			})}
		</View>
	);
}
