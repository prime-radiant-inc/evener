import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type {
	MutationAttachmentRef,
	MutationOptimisticRecord,
	MutationOutboxRecord,
	MutationRecoveryRecord,
} from "@evener/appwire-client/state/mutation";
import type { NativeMutationPersistenceSnapshot } from "./nativeMutationRuntime";
import {
	MutationRecoveryPanel,
	nativeMutationRecoveryActions,
	projectNativeMutationRecovery,
} from "./MutationRecoveryPanel";
import { render } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

const TARGET_A = JSON.stringify(["hub-a", "ref-a"]);
const TARGET_B = JSON.stringify(["hub-b", "ref-b"]);

type AnyRecord =
	| MutationOutboxRecord<MutationAttachmentRef>
	| MutationOptimisticRecord<MutationAttachmentRef>
	| MutationRecoveryRecord<MutationAttachmentRef>;

function record(
	targetRef: string,
	clientMutationId: string,
	intentSequence: number,
	state: "submitting" | "blockedUnknown" | "accepted" | "rejected" | "orphaned",
): AnyRecord {
	const base = {
		version: 1 as const,
		clientMutationId,
		targetRef,
		threadId: "thread",
		method: "turn/start",
		payload: { input: [{ type: "text", text: clientMutationId }] },
		attachments: [],
		optimisticDisplay: {},
		intentSequence,
		createdAt: intentSequence,
		composerText: clientMutationId,
	};
	if (state === "accepted") return { ...base, state };
	if (state === "rejected" || state === "orphaned")
		return { ...base, state: "blockedUnknown", recoveryKind: state };
	return { ...base, state };
}

function snapshot(records: AnyRecord[]): NativeMutationPersistenceSnapshot {
	return {
		outbox: records.filter(
			(record): record is MutationOutboxRecord<MutationAttachmentRef> =>
				!("recoveryKind" in record) &&
				(record.state === "submitting" || record.state === "blockedUnknown"),
		),
		optimistic: records.filter(
			(record): record is MutationOptimisticRecord<MutationAttachmentRef> =>
				record.state === "accepted",
		),
		recovery: records.filter(
			(record): record is MutationRecoveryRecord<MutationAttachmentRef> =>
				"recoveryKind" in record,
		),
	};
}

it("projects every durable state for one exact composite target in intent order", () => {
	const rows = projectNativeMutationRecovery(
		TARGET_A,
		snapshot([
			record(TARGET_A, "blocked", 4, "blockedUnknown"),
			record(TARGET_B, "other", 1, "rejected"),
			record(TARGET_A, "rejected", 3, "rejected"),
			record(TARGET_A, "accepted", 2, "accepted"),
			record(TARGET_A, "sending", 1, "submitting"),
			record(TARGET_A, "orphaned", 5, "orphaned"),
		]),
	);

	expect(rows.map((row) => [row.clientMutationId, row.status])).toEqual([
		["sending", "sending"],
		["accepted", "accepted"],
		["rejected", "rejected"],
		["blocked", "deliveryUnconfirmed"],
		["orphaned", "orphaned"],
	]);
	expect(rows.every((row) => row.targetKey === TARGET_A)).toBe(true);
});

it("assigns only the approved actions to each durable state", () => {
	expect(nativeMutationRecoveryActions("sending")).toEqual([]);
	expect(nativeMutationRecoveryActions("accepted")).toEqual([]);
	expect(nativeMutationRecoveryActions("deliveryUnconfirmed")).toEqual([]);
	expect(nativeMutationRecoveryActions("rejected")).toEqual(["restore", "dismiss"]);
	expect(nativeMutationRecoveryActions("orphaned")).toEqual(["copy", "dismiss"]);
});

it("invokes typed rejected actions and exposes no orphaned restore", () => {
	const onRestore = vi.fn();
	const onDismiss = vi.fn();
	const onCopy = vi.fn();
	const props = {
		targetKey: TARGET_A,
		snapshot: snapshot([
			record(TARGET_A, "rejected", 1, "rejected"),
			record(TARGET_A, "orphaned", 2, "orphaned"),
		]),
		actions: { onRestore, onDismiss, onCopy },
	} satisfies ComponentProps<typeof MutationRecoveryPanel>;
	const tree = render(<MutationRecoveryPanel {...props} />);
	const buttons = tree.root.findAllByProps({ accessibilityRole: "button" });
	const labels = buttons.map((button) => button.props.accessibilityLabel);
	expect(labels).toEqual(["Restore to draft", "Dismiss", "Copy", "Dismiss"]);
	expect(labels).not.toContain("Retry");

	act(() => {
		buttons[0]?.props.onPress();
		buttons[2]?.props.onPress();
	});
	expect(onRestore).toHaveBeenCalledWith(expect.objectContaining({
		clientMutationId: "rejected",
		targetKey: TARGET_A,
		status: "rejected",
	}));
	expect(onCopy).toHaveBeenCalledWith(expect.objectContaining({
		clientMutationId: "orphaned",
		targetKey: TARGET_A,
		status: "orphaned",
	}));
});
