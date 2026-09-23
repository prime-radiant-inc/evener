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
	record: MutationRecoveryRecord<MutationAttachmentRef>;
	actions: readonly NativeMutationRecoveryAction[];
}

const labels = {
	rejected: "Rejected",
	orphaned: "Needs review",
} satisfies Record<NativeMutationRecoveryStatus, string>;

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
	return input
		.filter(
			(item): item is { type: "text"; text: string } =>
				typeof item === "object" &&
				item !== null &&
				(item as { type?: unknown }).type === "text" &&
				typeof (item as { text?: unknown }).text === "string",
		)
		.map((item) => item.text)
		.join("\n");
}

// A rejected row restores only when there is text to restore; an orphaned row
// has no daemon message to replay and is only ever discarded. The discard
// offer is always present, so a row can never be stuck with no way out.
export function nativeMutationRecoveryActions(
	status: NativeMutationRecoveryStatus,
	hasText: boolean,
): readonly NativeMutationRecoveryAction[] {
	if (status === "rejected" && hasText) return ["restore", "discard"];
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
			record,
			actions: nativeMutationRecoveryActions(
				record.recoveryKind,
				text.length > 0,
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
	readError: unknown;
	/** An acquisition or discard failure that the read projection cannot carry. */
	failure: unknown;
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
	const [failure, setFailure] = useState<unknown>(null);
	const [attempt, setAttempt] = useState(0);

	useEffect(() => {
		if (!connected || runtime !== null) return;
		try {
			setRuntime(acquire());
		} catch (error) {
			setFailure(error);
		}
	}, [connected, runtime, acquire, attempt]);

	const projection = useNativeMutationRecovery(runtime, targetKey);
	const retry = useCallback(() => {
		setFailure(null);
		setAttempt((value) => value + 1);
	}, []);
	const discard = useCallback(
		(row: NativeMutationRecoveryRow) => {
			void discardRecoveredMutation(projection, row).then(
				() => undefined,
				(error) => setFailure(error),
			);
		},
		[projection],
	);

	return {
		targetKey,
		snapshot: projection.snapshot,
		loading: projection.loading,
		readError: projection.error,
		failure,
		count: projection.snapshot?.recovery.length ?? 0,
		retry,
		discard,
	};
}

export function MutationRecoveryPanel({
	targetKey,
	snapshot,
	error,
	actions,
}: {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	error: unknown;
	actions: MutationRecoveryActions;
}) {
	const colors = useColors();
	if (error) {
		return <ErrorMessage message={recoveryFailureMessage(error)} />;
	}
	if (snapshot === null) return <Copy muted>Loading delivery status…</Copy>;
	const rows = projectNativeMutationRecovery(targetKey, snapshot);
	if (rows.length === 0) return <Copy muted>No messages need recovery.</Copy>;
	return (
		<View style={{ gap: 12 }}>
			{rows.map((row) => (
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
								onPress={() =>
									action === "restore"
										? actions.onRestore(row)
										: actions.onDiscard(row)
								}
							>
								{action === "restore" ? "Restore to draft" : "Discard"}
							</Action>
						))}
					</View>
				</View>
			))}
		</View>
	);
}
