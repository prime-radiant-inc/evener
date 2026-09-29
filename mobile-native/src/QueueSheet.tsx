// The Queue sheet (ruling 37): every queued message, when more are waiting
// than the three above the composer show (ruling 18). It is a formSheet route
// over the session, so it reads the session's queue through the host the
// screen provides, and follows it live.
import { useNavigation } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect } from "react";
import { ScrollView } from "react-native";
import type { Routes } from "./screens";
import { GhostBubble } from "./session/GhostBubble";
import type { Ghost, GhostAction } from "./session/ghosts";
import { Sheet } from "./sheet/Sheet";
import { useSheet } from "./sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "./sheet/sheetHosts";
import { Toast, type ToastMessage, useToast } from "./Toast";
import { Action } from "./ui";

export interface QueueHost {
	/** The session's queued messages, every one of them. */
	ghosts: readonly Ghost[];
	disabled: boolean;
	/** Runs one ghost's action on the live queue, and says what to tell you. */
	act(ghost: Ghost, action: GhostAction): Promise<ToastMessage | null>;
	/** Shows a toast on the session, for an action that closed the sheet. */
	showOnSession(message: ToastMessage): void;
	/** Steers with the whole queue, when the session can. */
	steerAll?: () => Promise<ToastMessage | null>;
}

export const queueHosts = sheetHosts<QueueHost>();

export function QueueSheet({ route }: NativeStackScreenProps<Routes, "QueueSheet">) {
	const { hubId, ref } = route.params;
	const navigation = useNavigation();
	const sheet = useSheet();
	const toast = useToast();
	const host = useSheetHost(queueHosts, sheetKey(hubId, ref), sheet);
	const empty = host !== undefined && host.ghosts.length === 0;
	useEffect(() => {
		if (empty) sheet.finish();
	}, [empty, sheet]);
	function run(ghost: Ghost, action: GhostAction) {
		if (!host) return;
		// Edit puts the message in the composer, so the sheet gets out of the
		// way first and the composer's field can take the keyboard.
		if (action === "edit") {
			sheet.finish(() => {
				navigation.goBack();
				void host.act(ghost, action).then((message) => {
					if (message) host.showOnSession(message);
				});
			});
			return;
		}
		void host.act(ghost, action).then((message) => {
			if (message) toast.show(message);
		});
	}
	const steerAll = host?.steerAll;
	return (
		<Sheet
			title="Queued messages"
			done={{ onPress: () => sheet.finish() }}
			accessory={<Toast toast={toast.toast} dismiss={toast.dismiss} />}
		>
			<ScrollView contentContainerStyle={{ padding: 16, gap: 12 }}>
				{/* Every ghost here is queued, and a queued message's Edit merges
				into whatever is typed, so the composer never blocks it. */}
				{host?.ghosts.map((ghost) => (
					<GhostBubble
						key={ghost.key}
						ghost={ghost}
						disabled={host.disabled}
						canEdit
						editHint={null}
						backdrop="canvas"
						onAction={(action) => run(ghost, action)}
					/>
				))}
				{steerAll ? (
					<Action
						disabled={host.disabled}
						onPress={() => {
							void steerAll().then((message) => {
								if (message) toast.show(message);
							});
						}}
					>
						Steer all now
					</Action>
				) : null}
			</ScrollView>
		</Sheet>
	);
}
