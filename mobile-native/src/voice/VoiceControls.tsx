import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Pressable, ScrollView, View, useWindowDimensions } from "react-native";
import { SymbolView, type SFSymbol } from "expo-symbols";
import { Button, Row } from "../sheet/Grouped";
import { haptic } from "../haptics";
import { Copy, ErrorMessage, useColors, useTextScale } from "../ui";
import { defaultVoiceOptions, type VoiceClient, type VoiceSnapshot, type VoiceTarget, VoiceError } from "./voiceClient";
import { useVoiceClient } from "./voiceContext";
import { type VoicePhase, voicePresentation } from "./voicePresentation";
import { WakeTrialControls } from "./WakeTrialControls";

/** How long the bar says it heard the wake word: long enough to read at a
 * glance, short enough that it never hides the next state for long. */
const HEARD_FOR_MS = 1200;

export function VoiceControls({ target }: { target: VoiceTarget }) {
	const client = useVoiceClient();
	if (!client || !target.voiceAvailable) return null;
	return <Controls client={client} target={target} />;
}

type Ink = "inkHi" | "inkMid" | "aliveInk" | "attentionInk" | "dangerInk";

const TONES: Record<VoicePhase, { symbol: SFSymbol; ink: Ink }> = {
	disarmed: { symbol: "mic.slash", ink: "inkMid" },
	listening: { symbol: "mic.fill", ink: "aliveInk" },
	heard: { symbol: "checkmark.circle.fill", ink: "aliveInk" },
	talking: { symbol: "waveform", ink: "aliveInk" },
	speaking: { symbol: "speaker.wave.2.fill", ink: "inkHi" },
	recovering: { symbol: "arrow.clockwise", ink: "attentionInk" },
	error: { symbol: "exclamationmark.triangle.fill", ink: "dangerInk" },
	trial: { symbol: "waveform", ink: "aliveInk" },
};

function Controls({ client, target }: { client: VoiceClient; target: VoiceTarget }) {
	const state = useSyncExternalStore(client.subscribe, client.getSnapshot);
	const { palette } = useColors();
	const { height } = useWindowDimensions();
	const [expanded, setExpanded] = useState(false);
	const [busy, setBusy] = useState(false);
	const [takeover, setTakeover] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [justHeard, setJustHeard] = useState(false);
	const seenDetections = useRef(state.wake?.detections ?? 0);
	useEffect(() => {
		const detections = state.wake?.detections ?? 0;
		if (detections <= seenDetections.current) {
			seenDetections.current = detections;
			return;
		}
		seenDetections.current = detections;
		setJustHeard(true);
		haptic("light");
		const timer = setTimeout(() => setJustHeard(false), HEARD_FOR_MS);
		return () => clearTimeout(timer);
	}, [state.wake?.detections]);
	const bound = client.getTarget();
	const armed = state.state !== "disarmed";
	const same = bound?.ref === target.ref && bound.hub.id === target.hub.id;
	const presented = voicePresentation(state, justHeard);
	const talkEnabled = presented.talkEnabled && same;
	// Playback is this device's, so Stop does not need this session's binding or
	// a live capture; everything else needs Talk's conditions.
	const actionEnabled =
		presented.primary === "stop" ? true : presented.primary === "arm" ? target.manager && !busy : talkEnabled;
	const arm = async (replace = false) => {
		setBusy(true);
		setError(null);
		try {
			await client.arm(target, defaultVoiceOptions, replace);
			setTakeover(false);
			haptic("success");
		} catch (failure) {
			setExpanded(true);
			if (failure instanceof VoiceError && failure.code === "binding_owned") setTakeover(true);
			else
				setError(
					`Voice could not start (${failure instanceof VoiceError ? failure.code : "native_audio_unavailable"}). Text chat still works.`,
				);
		} finally {
			setBusy(false);
		}
	};
	const primary = () => {
		haptic("selection");
		if (presented.primary === "arm") void arm();
		else if (presented.primary === "stop") client.stopSpeaking();
		else client.talk();
	};
	return (
		<View testID="voice-controls" style={{ gap: 8, paddingHorizontal: 12, paddingBottom: 6 }}>
			<VoiceBar
				phase={presented.phase}
				primary={presented.primary}
				primaryEnabled={actionEnabled}
				expanded={expanded}
				phrase={state.wake?.phrase ?? ""}
				speakerOff={state.speakerConsentRequired}
				boundRef={armed && !same ? bound?.ref : undefined}
				onPrimary={presented.phase === "disarmed" ? () => setExpanded(true) : primary}
				onToggle={() => setExpanded(!expanded)}
			/>
			{expanded ? (
				<View testID="voice-panel" style={{ gap: 12, borderRadius: 14, padding: 14, backgroundColor: palette.inset }}>
					<ScrollView
						style={{ maxHeight: Math.max(220, Math.round(height * 0.45)) }}
						contentContainerStyle={{ gap: 12 }}
						showsVerticalScrollIndicator={false}
						keyboardShouldPersistTaps="handled"
						nestedScrollEnabled
					>
						<PanelHeader
							phase={presented.phase}
							phrase={state.wake?.phrase ?? ""}
							speakerOff={state.speakerConsentRequired}
							boundRef={armed && !same ? bound?.ref : undefined}
						/>
						{armed ? null : <Details state={state} forceOpen />}
						<PrimaryControl primary={presented.primary} enabled={actionEnabled} onPress={primary} />
						<View style={{ flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 4 }}>
							{armed ? (
								<Button text quiet label="Turn voice off" onPress={() => void disarm(client, setError)} />
							) : null}
							{armed && presented.primary !== "stop" ? (
								<Button text quiet label="Stop speaking" onPress={() => client.stopSpeaking()} />
							) : null}
							{takeover && !armed ? (
								<Button text quiet label="Use this iPhone instead" disabled={busy} onPress={() => void arm(true)} />
							) : null}
						</View>
						{!target.manager && !armed ? <Copy muted>Voice needs a manager session.</Copy> : null}
						{state.speakerConsentRequired ? (
							<Row label="Allow replies on the iPhone speaker" onPress={() => client.allowSpeaker()} />
						) : null}
						<WakeReceipt state={state} />
						{armed ? <Details state={state} /> : null}
						<SessionDetails state={state} boundRef={bound?.ref} same={same} />
						<WakeTrialControls client={client} state={state} />
						<Copy muted>{footnote(state)}</Copy>
						{state.error ? <Copy muted>{errorLine(state, presented.phase)}</Copy> : null}
						<ErrorMessage message={error} />
					</ScrollView>
				</View>
			) : null}
		</View>
	);
}

async function disarm(client: VoiceClient, setError: (message: string) => void) {
	haptic("selection");
	try {
		await client.disarm();
	} catch {
		setError("Microphone stopped; hub revocation could not be confirmed.");
	}
}

/** The bar's one line: what voice is doing, one glyph, and at most one action.
 * It sits on the glass above the composer, so it paints no surface of its own. */
function VoiceBar({
	phase,
	primary,
	primaryEnabled,
	expanded,
	phrase,
	speakerOff,
	boundRef,
	onPrimary,
	onToggle,
}: {
	phase: VoicePhase;
	primary: "arm" | "talk" | "stop";
	primaryEnabled: boolean;
	expanded: boolean;
	phrase: string;
	speakerOff: boolean;
	boundRef?: string;
	onPrimary(): void;
	onToggle(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const tone = TONES[phase];
	const headline = boundRef ? `Voice is on for ${shortRef(boundRef)}` : barHeadline(phase, phrase, speakerOff);
	const actionVisible = phase === "disarmed" || phase === "listening" || phase === "speaking" || phase === "talking";
	return (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Voice"
				accessibilityValue={{ text: headline }}
				accessibilityHint="Opens voice controls"
				accessibilityState={{ expanded }}
				onPress={onToggle}
				style={({ pressed }) => ({
					flex: 1,
					minHeight: 44,
					flexDirection: "row",
					alignItems: "center",
					gap: 10,
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<SymbolView name={tone.symbol} size={20 * scale} tintColor={palette[tone.ink]} />
				<Copy numberOfLines={2}>{headline}</Copy>
				<View style={{ flex: 1 }} />
				<SymbolView name={expanded ? "chevron.down" : "chevron.up"} size={13} tintColor={palette.inkMid} />
			</Pressable>
			{actionVisible ? (
				<Button
					primary
					compact
					label={barAction(primary)}
					disabled={!primaryEnabled}
					accessibilityLabel={barAction(primary)}
					onPress={onPrimary}
				/>
			) : null}
		</View>
	);
}

function barAction(primary: "arm" | "talk" | "stop") {
	return primary === "arm" ? "Turn on" : primary === "talk" ? "Talk" : "Stop";
}

function shortRef(ref: string) {
	return ref.replace(/^local:/, "");
}

function barHeadline(phase: VoicePhase, phrase: string, speakerOff: boolean) {
	const wake = phrase ? `“${phrase}”` : "the wake word";
	switch (phase) {
		case "disarmed":
			return "Voice off";
		case "listening":
			return speakerOff ? "Listening · speaker off" : `Listening for ${wake}`;
		case "heard":
			return `Heard ${wake}`;
		case "talking":
			return "Listening to you";
		case "speaking":
			return "Evener is speaking";
		case "recovering":
			return "Reconnecting voice…";
		case "error":
			return "Voice stopped";
		case "trial":
			return "Testing wake word";
	}
}

function PanelHeader({
	phase,
	phrase,
	speakerOff,
	boundRef,
}: {
	phase: VoicePhase;
	phrase: string;
	speakerOff: boolean;
	boundRef?: string;
}) {
	const { palette } = useColors();
	const tone = TONES[phase];
	return (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 12 }}>
			<View
				style={{
					width: 38,
					height: 38,
					borderRadius: 12,
					alignItems: "center",
					justifyContent: "center",
					backgroundColor: palette.canvas,
				}}
			>
				<SymbolView name={tone.symbol} size={20} tintColor={palette[tone.ink]} />
			</View>
			<View style={{ flexShrink: 1, gap: 2 }}>
				<Copy>{boundRef ? `Voice is on for ${shortRef(boundRef)}` : barHeadline(phase, phrase, speakerOff)}</Copy>
				<Copy muted>
					{boundRef
						? "Talk starts a turn there. Open that session to talk to it."
						: phase === "disarmed"
							? "Wake-word detection stays on this iPhone."
							: phase === "listening"
								? "Tap Talk, or just say the wake word."
								: phase === "trial"
									? "One attempt at a time, saved on this iPhone."
									: "Tap the bar to see what voice is doing."}
				</Copy>
			</View>
		</View>
	);
}

function PrimaryControl({
	primary,
	enabled,
	onPress,
}: {
	primary: "arm" | "talk" | "stop";
	enabled: boolean;
	onPress(): void;
}) {
	const label = primary === "arm" ? "Turn on voice" : primary === "talk" ? "Talk" : "Stop speaking";
	return <Button primary label={label} disabled={!enabled} accessibilityLabel={label} onPress={onPress} />;
}

/** The last detection, when there has been one: what the classifier heard and
 * how often, never its four-decimal score. */
function WakeReceipt({ state }: { state: VoiceSnapshot }) {
	const wake = state.wake;
	if (!wake?.detections) return null;
	return (
		<Copy muted>
			Heard wake · {wake.phrase || "wake word"} · {wake.detections} {wake.detections === 1 ? "time" : "times"}
		</Copy>
	);
}

/** The paragraphs that used to fill the panel, behind one row. */
function Details({ state, forceOpen = false }: { state: VoiceSnapshot; forceOpen?: boolean }) {
	const [open, setOpen] = useState(false);
	const shown = open || forceOpen;
	return (
		<View testID="voice-arm-disclosure" style={{ gap: 8 }}>
			{forceOpen ? (
				<Copy muted>Voice & privacy</Copy>
			) : (
				<Button
					text
					quiet
					label="Voice & privacy"
					expanded={open}
					onPress={() => {
						haptic("selection");
						setOpen(!open);
					}}
				/>
			)}
			{shown ? (
				<>
					<Copy muted>
						{`Anyone nearby can talk to Evener and hear replies, even when your iPhone is locked. “${
							state.wake?.phrase || "Hey Evener"
						}” is a phrase, not a voiceprint.`}
					</Copy>
					<Copy muted>
						{`When you talk, audio goes to ${state.provider || "OpenAI Realtime"}, which can be a different provider from the manager's model. It keeps audio for abuse monitoring for up to 30 days. Audio is not used for model training by default.`}
					</Copy>
				</>
			) : null}
		</View>
	);
}

function footnote(state: VoiceSnapshot) {
	const provider = state.provider || "OpenAI Realtime";
	if (state.cloudMicrophone) return `Audio is going to ${provider} now.`;
	return `Audio stays on this iPhone until you talk. Then it goes to ${provider}. Wake-word detection stays here.`;
}

/** What the native error means right now: capture stopped, audio reconnecting,
 * or a turn that ended while the phone kept listening. */
function errorLine(state: VoiceSnapshot, phase: VoicePhase) {
	if (phase === "error" || state.state === "disarmed") return `Voice stopped (${state.error}). Text chat still works.`;
	if (state.state === "recovering") return `Audio reconnecting (${state.error}).`;
	return `Last turn ended (${state.error}).`;
}

/** The spec's readout: bound session, audio route, whether the microphone is
 * local or in the cloud, and what voice heard, delivered and said. Behind one
 * row, because the transcript already carries the conversation. */
function SessionDetails({ state, boundRef, same }: { state: VoiceSnapshot; boundRef?: string; same: boolean }) {
	const [open, setOpen] = useState(false);
	const rows = [
		boundRef ? `Session · ${shortRef(boundRef)}${same ? "" : " (not this session)"}` : "",
		state.route ? `Audio · ${state.route}` : "",
		state.cloudMicrophone ? "Microphone · sending cloud audio" : "Microphone · local only",
		state.interpreted ? `Heard · ${state.interpreted}` : "",
		state.custody ? `Delivery · ${state.custody}` : "",
		state.canonical ? `Reply · ${state.canonical}` : "",
	].filter(Boolean);
	return (
		<View style={{ gap: 8 }}>
			<Button
				text
				quiet
				label="Session & audio"
				expanded={open}
				onPress={() => {
					haptic("selection");
					setOpen(!open);
				}}
			/>
			{open
				? rows.map((row) => (
						<Copy key={row} muted>
							{row}
						</Copy>
					))
				: null}
		</View>
	);
}
