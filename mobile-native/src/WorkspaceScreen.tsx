import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as SecureStore from "expo-secure-store";
import { useEffect, useState } from "react";
import { ActivityIndicator, View } from "react-native";
import { useConnection } from "./ConnectionProvider";
import { type HubProfile, HubProfiles } from "./connection";
import { HubWebView } from "./hubWeb/HubWebView";
import { isWorkspacePath } from "./location";
import type { Routes } from "./screens";
import { Button } from "./sheet/Grouped";
import { ErrorMessage } from "./ui";

const hubs = new HubProfiles(SecureStore);
type Props = NativeStackScreenProps<Routes, "Workspace">;

export function WorkspaceScreen({ route, navigation }: Props) {
	const { activeProfile } = useConnection();
	if (!activeProfile) return null;
	return <PairedWorkspace key={activeProfile.id} profile={activeProfile} route={route} navigation={navigation} />;
}

function PairedWorkspace({ profile, route, navigation }: Props & { profile: HubProfile }) {
	const [credential, setCredential] = useState<{ token: string; version: number } | null>(null);
	const [failed, setFailed] = useState(false);
	const [attempt, setAttempt] = useState(0);
	useEffect(() => {
		let current = true;
		setFailed(false);
		// Saving an edited credential publishes a fresh profile. An older read
		// must never replace the credential after an edit or a hub switch.
		hubs.token(profile.id).then(
			(value) => {
				if (current)
					setCredential((prior) =>
						prior?.token === value ? prior : { token: value, version: (prior?.version ?? 0) + 1 },
					);
			},
			() => {
				if (current) setFailed(true);
			},
		);
		return () => {
			current = false;
		};
	}, [profile, attempt]);
	const openHub = () => navigation.navigate("Hub", { screen: "HubHome", params: { hubId: profile.id } });
	if (failed || credential === null) {
		return (
			<View style={{ flex: 1, justifyContent: "center", padding: 24 }}>
				{failed ? (
					<>
						<ErrorMessage message="The saved hub credential couldn’t be read." />
						<Button label="Try again" onPress={() => setAttempt((value) => value + 1)} />
						<Button label="Hubs on this iPhone" onPress={openHub} />
					</>
				) : (
					<ActivityIndicator accessibilityLabel="Opening workspace" />
				)}
			</View>
		);
	}
	const path = route.params?.hubId === profile.id && isWorkspacePath(route.params.path) ? route.params.path : "/";
	return (
		<HubWebView
			key={credential.version}
			profile={profile}
			token={credential.token}
			initialPath={path}
			onPath={(next) => {
				if (isWorkspacePath(next) && (route.params?.hubId !== profile.id || next !== path)) {
					navigation.setParams({ hubId: profile.id, path: next });
				}
			}}
			onHubMenu={openHub}
			onDeviceWork={() => navigation.navigate("Sessions")}
		/>
	);
}
