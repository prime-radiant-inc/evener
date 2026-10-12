import {
	createPairingClaim,
	openPairingEnvelope,
	requestPairingClaim,
	type DevicePairingInvitation,
} from "@evener/appwire-client/pairing";
import { useNavigation } from "@react-navigation/native";
import { getRandomBytes } from "expo-crypto";
import { fetch as nativeFetch } from "expo/fetch";
import { useEffect, useRef, useState } from "react";
import { AppState } from "react-native";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import type { PairingTarget } from "./pairing";

/** Pending setup never enters the saved Hub store. Approval only opens review. */
export function PairingApproval({
	invitation,
	onApproved,
	onCancel,
}: {
	invitation: DevicePairingInvitation;
	onApproved(target: PairingTarget): void;
	onCancel(): void;
}) {
	const navigation = useNavigation();
	const [comparison, setComparison] = useState("");
	const [status, setStatus] = useState("Connecting to the Hub…");
	const approved = useRef(onApproved);
	const cancelPairing = useRef<(() => void) | null>(null);
	approved.current = onApproved;
	useEffect(() => {
		const random = getRandomBytes(32),
			claim = createPairingClaim(invitation, random);
		random.fill(0);
		setComparison(claim.comparison);
		let disposed = false,
			settled = false,
			foreground = AppState.currentState === "active",
			generation = 0;
		let timer: ReturnType<typeof setTimeout> | undefined, deadline: ReturnType<typeof setTimeout> | undefined;
		let request: AbortController | undefined;
		const isVisible = () => foreground && navigation.isFocused();
		const stop = () => {
			generation++;
			clearTimeout(timer);
			clearTimeout(deadline);
			request?.abort();
		};
		const cancel = () => {
			settled = true;
			stop();
			claim.secretKey.fill(0);
		};
		cancelPairing.current = cancel;
		const poll = async () => {
			if (disposed || settled || !isVisible()) return;
			const current = ++generation,
				controller = new AbortController();
			request = controller;
			deadline = setTimeout(() => controller.abort(), 10000);
			try {
				const reply = await requestPairingClaim(claim, "mobile", controller.signal, nativeFetch);
				if (disposed || current !== generation || !isVisible()) return;
				if (reply.state === "approved" && reply.envelope) {
					const target = openPairingEnvelope(claim, reply.envelope);
					settled = true;
					claim.secretKey.fill(0);
					approved.current(target);
					return;
				}
				if (reply.state === "expired" || reply.state === "rejected") {
					settled = true;
					claim.secretKey.fill(0);
					setStatus(
						reply.state === "expired"
							? "This invitation expired. Scan a new code from the Hub."
							: "A different device was approved. Create a new invitation and scan it again.",
					);
				} else setStatus("Compare this code with the one in Evener on your computer, then approve it there.");
			} catch (error) {
				if (!disposed && current === generation && isVisible())
					setStatus(error instanceof Error ? error.message : "Reconnecting to the Hub…");
			} finally {
				if (current === generation) {
					clearTimeout(deadline);
					if (!disposed && !settled && isVisible()) timer = setTimeout(() => void poll(), 1200);
				}
			}
		};
		const subscription = AppState.addEventListener("change", (state) => {
			foreground = state === "active";
			stop();
			if (foreground) void poll();
		});
		const unsubscribeFocus = navigation.addListener("focus", () => {
			stop();
			void poll();
		});
		const unsubscribeBlur = navigation.addListener("blur", () => {
			stop();
		});
		void poll();
		return () => {
			disposed = true;
			cancel();
			cancelPairing.current = null;
			subscription.remove();
			unsubscribeFocus();
			unsubscribeBlur();
		};
	}, [invitation, navigation]);
	return (
		<GroupedPage>
			<Group label="Pair with">
				<Row label={invitation.origin} />
			</Group>
			<Group label="Compare code">
				<Row label={comparison || "Connecting…"} machineLabel />
			</Group>
			<GroupFooter>{status}</GroupFooter>
			<Group>
				<Row
					label="Cancel pairing"
					tone="accent"
					onPress={() => {
						cancelPairing.current?.();
						onCancel();
					}}
				/>
			</Group>
		</GroupedPage>
	);
}
