// The Hub (spec 12): a large sheet from the Board's hub button that holds its
// own stack, so each page pushes inside the sheet (ruling 1). Pages read the
// hub, its client and the connection's readiness from the sheet's context
// (hubSheetContext.tsx).
import { randomUUID } from "expo-crypto";
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useMemo } from "react";
import { useConnection } from "../ConnectionProvider";
import { isReady } from "../connectionDisplay";
import { useHubFleet } from "../hosts/useHubFleet";
import { useRetainedScreenConnection } from "../retainedScreen";
import { HeaderButton } from "../sheet/HeaderButton";
import type { Routes } from "../screens";
import { useColors } from "../ui";
import { DetailLevelPage } from "./DetailLevelPage";
import { DisplayPage } from "./DisplayPage";
import { HostDetailPage } from "./HostDetailPage";
import { HostEditPage } from "./HostEditPage";
import { HostsPage } from "./HostsPage";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider, useClosesOnHubChange } from "./hubSheetContext";
import { useHubUpdates } from "./hubUpdates";

const HubStack = createNativeStackNavigator<HubRoutes>();

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile } = useConnection();
	const hubId = activeProfile?.id ?? "";
	const { state, canUseConnection, renderClient } = useRetainedScreenConnection(hubId);
	const { palette } = useColors();
	const close = useCallback(() => navigation.goBack(), [navigation]);
	const leave = useCallback(() => navigation.navigate("Hubs"), [navigation]);
	useClosesOnHubChange(hubId, close, leave);
	const ready = isReady(state);
	const updates = useHubUpdates(renderClient, ready);
	const { hosts, live } = useHubFleet(renderClient, randomUUID);
	const value = useMemo<HubSheetContextValue>(
		() => ({
			hubId,
			hubName: activeProfile?.name ?? "",
			client: renderClient,
			ready,
			canUseConnection,
			updates,
			hosts,
			live,
		}),
		[hubId, activeProfile?.name, renderClient, ready, canUseConnection, updates, hosts, live],
	);
	if (!activeProfile) return null;
	return (
		<HubSheetProvider value={value}>
			<HubStack.Navigator
				screenOptions={{
					headerStyle: { backgroundColor: palette.canvas },
					headerTintColor: palette.accentInk,
					headerTitleStyle: { color: palette.inkHi },
					headerShadowVisible: false,
					headerBackButtonDisplayMode: "minimal",
					contentStyle: { backgroundColor: palette.canvas },
				}}
			>
				<HubStack.Screen
					name="HubHome"
					component={HubHome}
					initialParams={{ hubId }}
					options={{
						title: activeProfile.name,
						headerRight: () => <HeaderButton label="Done" emphasized onPress={close} />,
					}}
				/>
				<HubStack.Screen name="Display" component={DisplayPage} options={{ title: "Display" }} />
				<HubStack.Screen name="DetailLevel" component={DetailLevelPage} options={{ title: "Default detail level" }} />
				<HubStack.Screen name="Hosts" component={HostsPage} options={{ title: "Hosts" }} />
				<HubStack.Screen
					name="HostDetail"
					component={HostDetailPage}
					options={({ route }) => ({ title: route.params.name })}
				/>
				{/* The page sets its own title and its Cancel and Save. */}
				<HubStack.Screen name="HostEdit" component={HostEditPage} />
			</HubStack.Navigator>
		</HubSheetProvider>
	);
}
