import { requireOptionalNativeModule } from "expo";
import * as SecureStore from "expo-secure-store";
import { useEffect, useState, type ReactNode } from "react";
import { AppState, Platform } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { HubProfiles } from "../connection";
import { VoiceClient, type NativeVoicePort } from "./voiceClient";
import { VoiceContext } from "./voiceContext";

export function VoiceProvider({ children }: { children: ReactNode }) {
	const { activeProfile, loading } = useConnection();
	const [voice] = useState(() => {
		const native = Platform.OS === "ios" ? requireOptionalNativeModule<NativeVoicePort>("EvenerVoice") : null;
		if (!native) return null;
		const hubs = new HubProfiles(SecureStore);
		return new VoiceClient(
			native,
			(id) => hubs.token(id),
			() => AppState.currentState === "active",
		);
	});
	useEffect(() => voice?.listen(), [voice]);
	useEffect(() => {
		if (!loading) voice?.retainHub(activeProfile?.id ?? null);
	}, [voice, loading, activeProfile?.id]);
	return <VoiceContext.Provider value={voice}>{children}</VoiceContext.Provider>;
}
