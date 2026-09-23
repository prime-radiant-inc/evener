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
import { useCallback, useEffect, useState } from "react";
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

function isImageInputItem(
	item: unknown,
): item is { type: "image"; data: string } {
	return (
		typeof item === "object" &&
		item !== null &&
		(item as { type?: unknown }).type === "image" &&
		typeof (item as { data?: unknown }).data === "string"
	);
}

// Whether the rejected mutation carries image inputs. Native intents persist
// the composer's `{type:"image", data}` items in the payload, so a restore that
// writes text only would drop them; the surface withholds restore for these
// rows instead.
export function recordCarriesAttachments(
	record: MutationRecoveryRecord<MutationAttachmentRef>,
): boolean {
	const input = record.payload.input;
	return Array.isArray(input) && input.some(isImageInputItem);
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

// A rejected row restores only when there is text to restore; an orphaned row
// has no daemon message to replay and is only ever discarded. The discard
// offer is always present, so a row can never be stuck with no way out.
export function nativeMutationRecoveryActions(
	status: NativeMutationRecoveryStatus,
	hasText: boolean,
	carriesAttachments = false,
): readonly NativeMutationRecoveryAction[] {
	if (status === "rejected" && hasText && !carriesAttachments)
		return ["restore", "discard"];
	return ["discard"];
}

export function projectNativeMutationRecovery(
	targetKey: string,
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null,
): NativeMutationRecoveryRow[] {
	if (snapshot === null) return [];
	const rows: NativeMutationRecoveryRow[] = [];
	for (const record of snapshot.recovery) {
		if (record.targetRef !== targetKey) continue;
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
			actions: nativeMutationRecoveryActions(
				record.recoveryKind,
				text.length > 0,
				carriesAttachments,
			),
		});
	}
	return rows.sort(
		(left, right) =>
			left.intentSequence - right.intentSequence ||
			left.clientMutationId.localeCompare(right.clientMutationId),
	);
}

export interface MutationRecoveryActions {
	/** Whether the composer can accept a restore right now: recovery must not
	 * silently do nothing when an existing draft or image would be clobbered. */
	canRestore(row: NativeMutationRecoveryRow): boolean;
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
	return error instanceof Error ? error.message : "Recovery is unavailable.";
}

// The recovery entry point: a healthy, connected conversation whose composer
// has no error and no uncertain submission still needs a way into the recovery
// modal when a target holds recoverable rows - a durable row outlives the
// transient error that surfaced it. A failed acquisition also offers the entry,
// so the failure can be retried in the modal instead of being invisible.
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
	const scope = `${targetKey}::${attempt}`;

	useEffect(() => {
		if (!connected || runtime !== null) return;
		try {
			setRuntime(acquire());
			setFailure((current) => (current?.scope === scope ? null : current));
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
			void discardRecoveredMutation(projection, row).then(
				() => setFailure((current) => (current?.scope === scope ? null : current)),
				(error) => setFailure({ scope, error }),
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
		loading: projection.loading,
		error,
		failed: error !== null && error !== undefined,
		count: projection.snapshot?.recovery.length ?? 0,
		retry,
		discard,
	};
}

export function MutationRecoveryPanel({
	targetKey,
	snapshot,
	error,
	onRetry,
	actions,
}: {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	error: unknown;
	onRetry?: () => void;
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
		return <Copy muted>Loading delivery status…</Copy>;
	}
	const rows = projectNativeMutationRecovery(targetKey, snapshot);
	return (
		<View style={{ gap: 12 }}>
			{failure}
			{rows.length === 0 ? (
				<Copy muted>No messages need recovery.</Copy>
			) : null}
			{rows.map((row) => {
				const offersRestore = row.actions.includes("restore");
				const restorable = offersRestore && actions.canRestore(row);
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
							{row.actions.map((action) => (
								<Action
									key={action}
									disabled={action === "restore" && !restorable}
									onPress={() => {
										if (action === "restore") {
											if (restorable) actions.onRestore(row);
										} else actions.onDiscard(row);
									}}
								>
									{action === "restore" ? "Restore to draft" : "Discard"}
								</Action>
							))}
						</View>
						{offersRestore && !restorable ? (
							<Copy muted>
								Clear or send your current draft to restore this message.
							</Copy>
						) : null}
						{!offersRestore && row.carriesAttachments ? (
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
