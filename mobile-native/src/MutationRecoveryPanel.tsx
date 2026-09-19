import type { MutationAttachmentRef, MutationRecord } from "@evener/appwire-client/state/mutation";
import { View } from "react-native";
import type { NativeMutationPersistenceSnapshot } from "./nativeMutationRuntime";
import { Action, Copy, styles, useColors } from "./ui";

export type NativeMutationRecoveryStatus =
	| "sending"
	| "accepted"
	| "deliveryUnconfirmed"
	| "rejected"
	| "orphaned";

export type NativeMutationRecoveryAction = "restore" | "dismiss" | "copy";
export type NativeMutationRecoveryRecord = MutationRecord<MutationAttachmentRef>;

export interface NativeMutationRecoveryRow {
	targetKey: string;
	clientMutationId: string;
	intentSequence: number;
	status: NativeMutationRecoveryStatus;
	label: string;
	reason?: string;
	record: NativeMutationRecoveryRecord;
	actions: readonly NativeMutationRecoveryAction[];
}

export interface MutationRecoveryActions {
	onRestore(row: NativeMutationRecoveryRow): void;
	onDismiss(row: NativeMutationRecoveryRow): void;
	onCopy(row: NativeMutationRecoveryRow): void;
}

const labels = {
	sending: "Sending", accepted: "Accepted", deliveryUnconfirmed: "Delivery unconfirmed",
	rejected: "Rejected", orphaned: "Needs review",
} satisfies Record<NativeMutationRecoveryStatus, string>;

export function nativeMutationRecoveryActions(
	status: NativeMutationRecoveryStatus,
): readonly NativeMutationRecoveryAction[] {
	if (status === "rejected") return ["restore", "dismiss"];
	if (status === "orphaned") return ["copy", "dismiss"];
	return [];
}

function row(
	targetKey: string,
	record: NativeMutationRecoveryRecord,
	status: NativeMutationRecoveryStatus,
	reason?: string,
): NativeMutationRecoveryRow {
	return {
		targetKey,
		clientMutationId: record.clientMutationId,
		intentSequence: record.intentSequence,
		status,
		label: labels[status],
		...(reason === undefined ? {} : { reason }),
		record,
		actions: nativeMutationRecoveryActions(status),
	};
}

export function projectNativeMutationRecovery(
	targetKey: string,
	snapshot: NativeMutationPersistenceSnapshot,
): NativeMutationRecoveryRow[] {
	const rows: NativeMutationRecoveryRow[] = [];
	for (const record of snapshot.outbox) {
		if (record.targetRef !== targetKey) continue;
		rows.push(
			row(
				targetKey,
				record,
				record.state === "blockedUnknown"
					? "deliveryUnconfirmed"
					: "sending",
			),
		);
	}
	for (const record of snapshot.optimistic) {
		if (record.targetRef === targetKey) rows.push(row(targetKey, record, "accepted"));
	}
	for (const record of snapshot.recovery) {
		if (record.targetRef !== targetKey) continue;
		rows.push(row(targetKey, record, record.recoveryKind, record.recoveryReason));
	}
	return rows.sort(
		(left, right) =>
			left.intentSequence - right.intentSequence ||
			left.clientMutationId.localeCompare(right.clientMutationId),
	);
}

export function MutationRecoveryPanel({
	targetKey,
	snapshot,
	actions,
}: {
	targetKey: string;
	snapshot: NativeMutationPersistenceSnapshot | null;
	actions: MutationRecoveryActions;
}) {
	const colors = useColors();
	if (snapshot === null) return <Copy muted>Loading delivery status…</Copy>;
	const rows = projectNativeMutationRecovery(targetKey, snapshot);
	if (rows.length === 0) return null;
	return (
		<View style={{ gap: 12 }}>
			{rows.map((row) => (
				<View
					key={row.clientMutationId}
					style={[styles.card, { borderColor: colors.border }]}
				>
					<Copy>{row.label}</Copy>
					{row.reason ? <Copy muted>{row.reason}</Copy> : null}
					{row.record.composerText ? (
						<Copy muted numberOfLines={3}>
							{row.record.composerText}
						</Copy>
					) : null}
					{row.actions.length ? (
						<View style={styles.row}>
							{row.actions.map((action) => (
								<Action
									key={action}
									onPress={() =>
										action === "restore"
											? actions.onRestore(row)
											: action === "dismiss"
												? actions.onDismiss(row)
												: actions.onCopy(row)
									}
								>
									{action === "restore"
										? "Restore to draft"
										: action === "dismiss"
											? "Dismiss"
											: "Copy"}
								</Action>
							))}
						</View>
					) : null}
				</View>
			))}
		</View>
	);
}
