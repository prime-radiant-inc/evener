// The approval dock (spec 8.4, rulings 12 and 38): an amber-edged card in the
// tray's place while the sandbox waits on your decision. It says what the
// agent wants, the tool and its target, the session's scope, then Allow and
// Deny. There is no redirect, as on the web's approval card.
import type { SandboxEscalationRequested } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Pressable, Text, View } from "react-native";
import type { ApprovalControls } from "../approvalControls";
import { fonts } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { approvalCard } from "./askDockCopy";
import { DockBody } from "./DockBody";
import { dockCard } from "./dockCard";

export interface ApprovalDockProps {
	request: SandboxEscalationRequested;
	/** Null while the hub is away: the dock still says what waits, and offers
	 * Allow and Deny again once they can act. */
	controls: ApprovalControls | null;
	/** A decision the hub took and the session re-read confirms. */
	onDecided(allowed: boolean): void;
	/** How many more approvals wait behind this one. Each takes the dock in
	 * turn, in the order the hub raised them. */
	waiting?: number;
}

// A path wraps only at its slashes: a zero-width space after each one gives
// the line a break there and nowhere else.
const breakAtSlashes = (path: string) => path.replaceAll("/", "/\u200b");

const NO_DECISION = { pending: null, refreshing: false, error: null };
const noDecision = () => NO_DECISION;
const noSubscription = () => () => {};

export function ApprovalDock({ request, controls, onDecided, waiting = 0 }: ApprovalDockProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const state = useSyncExternalStore(controls?.subscribe ?? noSubscription, controls?.getSnapshot ?? noDecision);
	const card = approvalCard(request);
	// A decision it couldn't confirm re-reads the session once on its own, so
	// there is no refresh to press. The error line stays until a read clears it
	// or a new press tries again.
	const refreshedFor = useRef(false);
	// A decision this dock sent holds it until the approval leaves (the screen
	// keys the dock by escalationId) or an error comes back. It lives here,
	// above the controls, because a reconnect replaces them mid-decision: the
	// disposed ones settle silently, and the new ones know nothing of it, so
	// without this a second press could resolve the same approval twice.
	const [decisionSent, setDecisionSent] = useState(false);
	const sentWith = useRef<ApprovalControls | null>(null);
	// After a reconnect the hub's fresh state decides. The new controls
	// re-read the session once; if the approval is still listed then (this
	// dock is still mounted), the first decision can't be known to have
	// landed, so you may decide again. The hub refuses a second resolve of an
	// escalation it already settled (agent/session_escalation.go's
	// ResolveSandboxEscalation), so deciding again never decides twice.
	useEffect(() => {
		if (!decisionSent || !controls || sentWith.current === null || controls === sentWith.current) return;
		sentWith.current = controls;
		// Another reconnect may replace these controls before their read
		// lands; only the current controls' read speaks for the hub now.
		let current = true;
		void controls.refresh().then(() => {
			if (current && controls.getSnapshot().error === null) setDecisionSent(false);
		});
		return () => {
			current = false;
		};
	}, [decisionSent, controls]);
	useEffect(() => {
		if (state.error === null) {
			refreshedFor.current = false;
			return;
		}
		setDecisionSent(false);
		if (refreshedFor.current || !controls) return;
		refreshedFor.current = true;
		void controls.refresh();
	}, [state.error, controls]);
	// An error holds Allow and Deny only until its re-read settles: then they
	// come back beside the error line, and a press tries again. Every step
	// publishes (the re-read starts and settles), so reading the ref here
	// sees each change.
	const awaitingReRead = state.error !== null && !refreshedFor.current;
	const off = decisionSent || state.pending !== null || state.refreshing || awaitingReRead;
	async function decide(allowed: boolean) {
		if (off || !controls) return;
		// resolve says nothing of its own: a decision went out when this
		// approval went pending.
		let sent = false;
		const stop = controls.subscribe(() => {
			if (controls.getSnapshot().pending !== request.escalationId) return;
			sent = true;
			sentWith.current = controls;
			setDecisionSent(true);
		});
		try {
			await controls.resolve(request, allowed);
		} finally {
			stop();
		}
		const settled = controls.getSnapshot();
		if (!sent) return;
		if (settled.error !== null) setDecisionSent(false);
		// It held when these controls settled with no error. Controls still
		// pending were disposed mid-decision: nothing confirmed it, so the
		// dock holds and says nothing.
		else if (settled.pending === null) onDecided(allowed);
	}
	const body = { allowFontScaling, style: { fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid } };
	return (
		// What the sandbox blocked scrolls when the screen has less room than it
		// needs (a long target, the largest text sizes); Allow and Deny stay on
		// screen.
		<View testID="approval-dock" style={{ ...dockCard(palette), padding: 16, gap: 8 }}>
			<DockBody contentContainerStyle={{ gap: 8 }}>
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
				<Text {...body}>{card.partiallyRan ? `${card.scope} Part of this may already have run.` : card.scope}</Text>
				{waiting > 0 ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={{
							fontSize: 13 * scale,
							lineHeight: 18 * scale,
							color: palette.inkMid,
							fontVariant: ["tabular-nums"],
						}}
					>
						{`${waiting} more waiting`}
					</Text>
				) : null}
			</DockBody>
			{controls && state.error ? (
				<Text {...body} numberOfLines={1} style={{ ...body.style, color: palette.dangerInk }}>
					{state.error}
				</Text>
			) : null}
			{/* Allow and Deny show only while they can act. */}
			{controls ? (
				<>
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
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.onFill }}
						>
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
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkHi }}
						>
							Deny
						</Text>
					</Pressable>
				</>
			) : null}
		</View>
	);
}
