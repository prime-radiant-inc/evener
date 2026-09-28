// The approval dock (spec 8.4, rulings 12 and 38): an amber-edged card in the
// tray's place while the sandbox waits on your decision. It says what the
// agent wants, the tool and its target, the session's scope, then Allow and
// Deny. There is no redirect, as on the web's approval card.
import type { SandboxEscalationRequested } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useEffect, useRef, useSyncExternalStore } from "react";
import { Pressable, Text, View } from "react-native";
import type { ApprovalControls } from "../approvalControls";
import { fonts } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { approvalCard } from "./askDockCopy";

export interface ApprovalDockProps {
	request: SandboxEscalationRequested;
	controls: ApprovalControls;
	/** A decision the hub took and the session re-read confirms. */
	onDecided(allowed: boolean): void;
}

// A path wraps only at its slashes: a zero-width space after each one gives
// the line a break there and nowhere else.
const breakAtSlashes = (path: string) => path.replaceAll("/", "/​");

export function ApprovalDock({ request, controls, onDecided }: ApprovalDockProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
	const card = approvalCard(request);
	// A decision it couldn't confirm re-reads the session once on its own, so
	// there is no refresh to press. The error stays until a read clears it.
	const refreshedFor = useRef(false);
	useEffect(() => {
		if (state.error === null) {
			refreshedFor.current = false;
			return;
		}
		if (refreshedFor.current) return;
		refreshedFor.current = true;
		void controls.refresh();
	}, [state.error, controls]);
	const off = state.pending !== null || state.refreshing || state.error !== null;
	async function decide(allowed: boolean) {
		if (off) return;
		// resolve says nothing of its own: a decision went out when this
		// approval went pending, and it held when no error followed.
		let sent = false;
		const stop = controls.subscribe(() => {
			if (controls.getSnapshot().pending === request.escalationId) sent = true;
		});
		try {
			await controls.resolve(request, allowed);
		} finally {
			stop();
		}
		if (sent && controls.getSnapshot().error === null) onDecided(allowed);
	}
	const body = { allowFontScaling, style: { fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid } };
	return (
		<View
			style={{
				marginHorizontal: 16,
				marginBottom: 8,
				padding: 16,
				gap: 8,
				borderWidth: 1,
				borderColor: palette.attentionEdge,
				borderRadius: 12,
				borderCurve: "continuous",
				backgroundColor: palette.surface,
			}}
		>
			<View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
				<SymbolView name="hand.raised.circle.fill" tintColor={palette.attention} size={22 * scale} />
				<Text
					allowFontScaling={allowFontScaling}
					style={{ flex: 1, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
				>
					{card.wants}
				</Text>
			</View>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontFamily: fonts.mono, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid }}
			>
				{card.target ? `${card.tool}  ${breakAtSlashes(card.target)}` : card.tool}
			</Text>
			<Text {...body}>
				{card.partiallyRan ? `${card.scope} Part of this may already have run.` : card.scope}
			</Text>
			{state.error ? (
				<Text {...body} numberOfLines={1} style={{ ...body.style, color: palette.dangerInk }}>
					{state.error}
				</Text>
			) : null}
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={card.primary.label}
				accessibilityHint={card.primary.detail}
				accessibilityState={{ disabled: off }}
				disabled={off}
				onPress={() => {
					void decide(true);
				}}
				style={({ pressed }) => ({
					minHeight: 44,
					marginTop: 4,
					paddingVertical: 8,
					paddingHorizontal: 16,
					alignItems: "center",
					justifyContent: "center",
					borderRadius: 12,
					borderCurve: "continuous",
					backgroundColor: palette.accentFill,
					opacity: off ? 0.4 : pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.onFill }}
				>
					{card.primary.label}
				</Text>
				<Text allowFontScaling={allowFontScaling} style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.onFill }}>
					{card.primary.detail}
				</Text>
			</Pressable>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Deny"
				accessibilityState={{ disabled: off }}
				disabled={off}
				onPress={() => {
					void decide(false);
				}}
				style={({ pressed }) => ({
					minHeight: 44,
					alignItems: "center",
					justifyContent: "center",
					opacity: off ? 0.4 : pressed ? 0.6 : 1,
				})}
			>
				<Text allowFontScaling={allowFontScaling} style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkHi }}>
					Deny
				</Text>
			</Pressable>
		</View>
	);
}
