import { useState, useSyncExternalStore } from "react";
import { View } from "react-native";
import { Row } from "../sheet/Grouped";
import { HeaderButton } from "../sheet/HeaderButton";
import { Copy, ErrorMessage } from "../ui";
import { defaultVoiceOptions, type VoiceClient, type VoiceTarget, VoiceError } from "./voiceClient";
import { useVoiceClient } from "./voiceContext";

export function VoiceControls({ target }: { target: VoiceTarget }) {
	const client = useVoiceClient();
	if (!client || !target.voiceAvailable) return null;
	return <Controls client={client} target={target} />;
}
function Controls({ client, target }: { client: VoiceClient; target: VoiceTarget }) {
	const state = useSyncExternalStore(client.subscribe, client.getSnapshot);
	const [expanded, setExpanded] = useState(false);
	const [busy, setBusy] = useState(false);
	const [takeover, setTakeover] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const bound = client.getTarget();
	const armed = state.state !== "disarmed";
	const same = bound?.ref === target.ref && bound.hub.id === target.hub.id;
	const arm = async (replace = false) => {
		setBusy(true);
		setError(null);
		try {
			await client.arm(target, defaultVoiceOptions, replace);
			setTakeover(false);
		} catch (failure) {
			if (failure instanceof VoiceError && failure.code === "binding_owned") setTakeover(true);
			else
				setError(
					`Voice could not arm (${failure instanceof VoiceError ? failure.code : "native_audio_unavailable"}). Text chat is unchanged.`,
				);
		} finally {
			setBusy(false);
		}
	};
	return (
		<View testID="voice-controls">
			<HeaderButton
				label={expanded ? "Hide voice controls" : "Voice · Talk preview"}
				onPress={() => setExpanded(!expanded)}
			/>
			{expanded ? (
				<>
					<Copy>Chief of staff · {bound?.ref ?? target.ref}</Copy>
					<Copy muted>
						{armed
							? state.capturing
								? state.cloudMicrophone
									? "Microphone · Sending cloud audio"
									: "Microphone · Local only"
								: "Listening paused"
							: "Disarmed · Talk activation, wake is not qualified"}
					</Copy>
					{state.route ? <Copy muted>Route: {state.route} · physical qualification pending</Copy> : null}
					{!target.manager && !armed ? (
						<Copy muted>Select an existing manager session, or explicitly start one with Start as Manager.</Copy>
					) : null}
					<View testID="voice-arm-disclosure">
						<Copy muted>
							Arming lets nearby people or recordings issue requests and hear replies, including while locked. Wake is
							not speaker identification.
						</Copy>
						<Copy muted>
							Voice uses{" "}
							{state.provider ? `the hub's ${state.provider} provider` : "the hub's OpenAI Realtime provider"}, separate
							from the manager's model provider. Audio is not used for API training by default, but is retained for
							abuse monitoring up to 30 days; store:false is not zero retention.
						</Copy>
					</View>
					<View style={{ flexDirection: "row", flexWrap: "wrap" }}>
						{armed ? (
							<HeaderButton
								label="Disarm"
								onPress={() => {
									void client
										.disarm()
										.catch(() => setError("Microphone stopped; hub revocation could not be confirmed."));
								}}
							/>
						) : (
							<HeaderButton
								label="Arm"
								disabled={!target.manager || busy}
								onPress={() => {
									void arm();
								}}
							/>
						)}
						<HeaderButton
							label="Talk"
							disabled={!armed || !same || !state.capturing || state.state === "recovering"}
							onPress={() => client.talk()}
						/>
						<HeaderButton label="Stop speaking" disabled={!armed} onPress={() => client.stopSpeaking()} />
					</View>
					{state.speakerConsentRequired ? (
						<Row label="Allow future speech on iPhone speaker" onPress={() => client.allowSpeaker()} />
					) : null}
					{takeover && !armed ? (
						<HeaderButton
							label="Take over the other armed device"
							disabled={busy}
							onPress={() => {
								void arm(true);
							}}
						/>
					) : null}
					{state.interpreted ? <Copy selectable>Interpreted: {state.interpreted}</Copy> : null}
					{state.custody ? <Copy muted>Delivery: {state.custody}</Copy> : null}
					{state.canonical ? <Copy selectable>Canonical reply: {state.canonical}</Copy> : null}
					<ErrorMessage
						message={error || (state.error ? `Voice paused (${state.error}). Text remains available.` : null)}
					/>
				</>
			) : null}
		</View>
	);
}
