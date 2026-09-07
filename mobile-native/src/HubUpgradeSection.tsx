import { useCallback } from "react";
import { ActivityIndicator, Alert, View } from "react-native";
import type { UpgradeState } from "./hubUpgrade";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export interface HubUpgradeSectionProps {
	hubName: string;
	state: UpgradeState;
	onStart: () => void;
	onRefresh: () => void;
	disabled?: boolean;
	runningIdentity?: { version?: string; commit?: string };
}

function Detail({ label, value }: { label: string; value?: string }) {
	return (
		<View style={{ gap: 2 }}>
			<Copy muted>{label}</Copy>
			<Copy>{value || "Unavailable"}</Copy>
		</View>
	);
}

function ResponseDetails({ state }: { state: UpgradeState }) {
	if (
		state.kind !== "installed" &&
		state.kind !== "uncertain" &&
		state.kind !== "storageUnavailable"
	)
		return null;
	const response = state.response;
	if (!response) return null;
	return (
		<View style={{ gap: 8 }}>
			<Copy>Installed update</Copy>
			<Detail label="Release installed" value={response.release} />
			<Detail label="Channel" value={response.channel} />
			<Copy>{response.restartMessage}</Copy>
		</View>
	);
}

export function HubUpgradeSection({
	hubName,
	state,
	onStart,
	onRefresh,
	disabled = false,
	runningIdentity,
}: HubUpgradeSectionProps) {
	const colors = useColors();
	const confirmStart = useCallback(() => {
		Alert.alert(
			`Upgrade ${hubName}?`,
			`Install an update on ${hubName}. The hub may need to restart before it runs.`,
			[
				{ text: "Cancel", style: "cancel" },
				{ text: "Upgrade", style: "destructive", onPress: onStart },
			],
		);
	}, [hubName, onStart]);
	const overview = "overview" in state ? state.overview : undefined;
	const running = overview?.hub ?? runningIdentity;
	const error =
		state.kind === "uncertain" || state.kind === "storageUnavailable"
			? state.message
			: null;

	return (
		<View style={[styles.card, { borderColor: colors.border }]}>
			<Copy>{hubName} upgrade</Copy>
			<View style={{ gap: 8 }}>
				<Copy muted>Running now</Copy>
				<Detail label="Version" value={running?.version} />
				<Detail label="Commit" value={running?.commit} />
			</View>
			<ResponseDetails state={state} />
			{state.kind === "running" && (
				<View style={styles.row}>
					<ActivityIndicator accessibilityLabel="Installing hub update" />
					<Copy>Installing update…</Copy>
				</View>
			)}
			{state.kind === "idle" && (
				<Action disabled={disabled} onPress={confirmStart}>
					Upgrade hub
				</Action>
			)}
			{state.kind === "uncertain" && (
				<Copy muted>
					The update may have been installed. Refresh to check the running
					version.
				</Copy>
			)}
			{state.kind === "storageUnavailable" && (
				<Copy muted>
					The update status could not be read or saved on this device. Reopen
					Hub Settings to check it.
				</Copy>
			)}
			{(state.kind === "installed" || state.kind === "uncertain") && (
				<Action disabled={disabled} tone="quiet" onPress={onRefresh}>
					Refresh running version
				</Action>
			)}
			<ErrorMessage message={error} />
		</View>
	);
}
