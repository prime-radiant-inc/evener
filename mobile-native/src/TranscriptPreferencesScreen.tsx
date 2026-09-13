import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useConnection } from "./ConnectionProvider";
import { useNativePreferences } from "./NativePreferencesProvider";
import type { Routes } from "./screens";
import { TranscriptPreferencesEditor } from "./TranscriptPreferencesEditor";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function TranscriptPreferencesScreen({
	route,
}: NativeStackScreenProps<Routes, "TranscriptPreferences">) {
	const connection = useConnection();
	const preferences = useNativePreferences();
	const colors = useColors();
	const [error, setError] = useState<string | null>(null);
	if (connection.activeProfile?.id !== route.params.hubId)
		return (
			<Copy>This hub is no longer selected. Return to Hubs to reconnect.</Copy>
		);
	const model = preferences.model;
	const run = (action: () => unknown) => {
		setError(null);
		Promise.resolve()
			.then(action)
			.catch(() => {
				setError(
					"The change could not be completed. Review the settings and try again.",
				);
			});
	};
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			<ErrorMessage message={error} />
			{!preferences.connected ? (
				<View style={{ paddingHorizontal: 20 }}>
					<Action onPress={connection.retry}>Reconnect</Action>
				</View>
			) : null}
			{preferences.snapshot ? (
				<TranscriptPreferencesEditor
					key={route.params.hubId}
					hubName={connection.activeProfile.name}
					state={preferences.snapshot.transcriptMobile}
					connected={preferences.connected}
					edit={(config) => {
						if (model) run(() => model.editTranscript(config));
					}}
					save={() => {
						if (model) run(() => model.saveTranscript());
					}}
					refresh={() => {
						if (model) run(() => model.refresh());
					}}
					discard={() => {
						if (model) run(() => model.discardTranscriptDraft());
					}}
					rebase={(revision) => {
						if (model) run(() => model.rebaseTranscriptDraft(revision));
					}}
				/>
			) : (
				<View style={{ padding: 20 }}>
					<Copy>
						Connect to {connection.activeProfile.name} to load transcript
						settings.
					</Copy>
				</View>
			)}
		</SafeAreaView>
	);
}
