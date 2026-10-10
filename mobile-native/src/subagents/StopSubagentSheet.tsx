// Ask coordinator to stop it (spec 9, ruling 10): a prefilled, editable
// message to the subagent's coordinator, and the one Send that steers,
// because the request is about the turn that is running. It is admitted
// through the durable runtime like the composer's messages. A hub that can
// stop a subagent directly (S6) never opens this sheet: the subagent bar's
// "Stop subagent" asks for a confirmation instead.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useState } from "react";
import { ScrollView, Text, TextInput, View } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { getNativeMutationRuntime } from "../nativeMutationRuntime";
import type { Routes } from "../screens";
import { SendButton } from "../session/SendButton";
import { SessionLink, stopRequestKind, submitSessionMessage } from "../session/sessionMessage";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { stopRequests } from "./nativeStopRequests";
import { flattenSubagents, type SubagentRow } from "./subagentModel";
import { useCoordinatorState } from "./useCoordinatorState";
import { useSubagentTree } from "./useSubagentTree";

function prefill(row: SubagentRow): string {
	return `Stop subagent “${row.title}”: ${row.state === "failed" ? "it has failed" : "it's no longer needed"}.`;
}

export function StopSubagentSheet({ route }: NativeStackScreenProps<Routes, "StopSubagentSheet">) {
	const { hubId, coordinator, ref } = route.params;
	const { client, state, activeProfile } = useConnection();
	// Only this hub's own connection reads the coordinator and sends to it.
	const online = state === "ready" && client !== null && activeProfile?.id === hubId;
	const { palette } = useColors();
	const scale = useTextScale();
	// The coordinator's state, read without taking the connection's
	// subscription, which the transcript under this sheet follows. A read
	// names its connection's instance, so each connection reads again.
	const coordinatorRead = useCoordinatorState(online ? client : null, coordinator.ref);
	const coordinatorState = coordinatorRead === "unreadable" ? null : coordinatorRead;
	// The tree is the coordinator's as it reads now: a coordinator that
	// restarted since the sheet opened runs under a new thread, and
	// the shared activity context identifies the resolved session.
	const { snapshot } = useSubagentTree(hubId, coordinator.ref, coordinatorState?.threadId ?? coordinator.threadId);
	const row = useMemo(
		() =>
			snapshot.tree ? (flattenSubagents(snapshot.tree).find((candidate) => candidate.ref === ref) ?? null) : undefined,
		[snapshot.tree, ref],
	);

	// The message starts as the prefill once the row is known; editing it
	// makes it unsaved input. The prefill is written once, from the row as it first reads, so neither
	// the words nor "edited" shift when the tree moves under the sheet.
	const [seed, setSeed] = useState<string | null>(null);
	useEffect(() => {
		if (row && seed === null) setSeed(prefill(row));
	}, [row, seed]);
	const [text, setText] = useState<string | null>(null);
	const words = text ?? seed ?? "";
	const edited = text !== null && text !== seed;
	const [failure, setFailure] = useState<string | null>(null);
	const [sending, setSending] = useState(false);
	const sheet = useSheet({ dirty: edited, discardTitle: "Discard this message?" });

	// A subagent the whole tree no longer lists has nothing left to stop. A
	// tree that's only partly listed may hold it in what's missing.
	useEffect(() => {
		// An edited message stays for you to keep or discard.
		if (row === null && !snapshot.partial && !edited) sheet.finish();
	}, [row, snapshot.partial, edited, sheet]);

	// With S6 the subagent bar offers "Stop subagent", with a confirmation
	// and no message, stopping only that subagent; it opens this sheet only
	// for a coordinator that doesn't advertise stopSubagent.
	const kind = coordinatorState ? stopRequestKind(coordinatorState) : null;

	const canSend = online && !sending && words.trim() !== "" && kind !== null && row !== undefined && row !== null;
	const submit = async () => {
		if (!canSend || !client || !row) return;
		setSending(true);
		setFailure(null);
		try {
			// The coordinator may have finished its turn, or restarted, since
			// the sheet read it: Send asks again, so a stop steers only a turn
			// that's still running, and names the instance that's there now.
			const link = new SessionLink(client, coordinator.ref);
			const now = await link.read({ follow: false }).finally(() => link.dispose());
			const kindNow = stopRequestKind(now);
			if (!kindNow) throw new Error("The coordinator can't take a message right now.");
			await submitSessionMessage(
				getNativeMutationRuntime(),
				client,
				{ hubId, ref: coordinator.ref, threadId: now.threadId, instanceId: now.instanceId },
				kindNow,
				words.trim(),
			);
		} catch (error) {
			setFailure(error instanceof Error ? error.message : String(error));
			setSending(false);
			return;
		}
		stopRequests(hubId).request(coordinator.ref, row, Date.now());
		sheet.finish();
	};

	const small = { fontSize: 13 * scale, lineHeight: 18 * scale };
	return (
		<Sheet title="Stop subagent" onCancel={sheet.close}>
			<ScrollView
				automaticallyAdjustKeyboardInsets
				keyboardDismissMode="on-drag"
				contentContainerStyle={{ padding: 16, gap: 8 }}
			>
				<View style={{ flexDirection: "row", alignItems: "flex-end", gap: 8 }}>
					<TextInput
						accessibilityLabel="Message to the coordinator"
						multiline
						value={words}
						onChangeText={setText}
						allowFontScaling={allowFontScaling}
						style={{
							flex: 1,
							minHeight: 88,
							color: palette.inkHi,
							fontSize: 17 * scale,
							lineHeight: 24 * scale,
							textAlignVertical: "top",
						}}
					/>
					<SendButton label="Send stop request" disabled={!canSend} onPress={() => void submit()} />
				</View>
				<Text allowFontScaling={allowFontScaling} style={{ ...small, color: palette.inkMid }}>
					{!online
						? "Send when you're back online."
						: coordinatorRead === "unreadable" || (coordinatorState && kind === null)
							? "The coordinator can't take a message right now."
							: "Arrives at the coordinator's next step"}
				</Text>
				{failure !== null ? (
					<Text allowFontScaling={allowFontScaling} style={{ ...small, color: palette.dangerInk }}>
						{`Couldn't send this: ${failure}`}
					</Text>
				) : null}
			</ScrollView>
		</Sheet>
	);
}
