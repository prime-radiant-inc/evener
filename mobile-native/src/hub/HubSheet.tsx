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
import { HubSettingsScreen } from "../HubSettingsScreen";
import { KeybindingPreferencesScreen } from "../KeybindingPreferencesScreen";
import { LaunchSettingsScreen } from "../LaunchSettingsScreen";
import { useRetainedScreenConnection } from "../retainedScreen";
import { HeaderButton } from "../sheet/HeaderButton";
import { sheetStackOptions } from "../sheet/sheetStack";
import type { Routes } from "../screens";
import { useColors } from "../ui";
import { AddHubPage } from "./AddHubPage";
import { AlertsPage } from "./AlertsPage";
import { DetailLevelPage } from "./DetailLevelPage";
import { DisplayPage } from "./DisplayPage";
import { HostDetailPage } from "./HostDetailPage";
import { HostEditPage } from "./HostEditPage";
import { HostsPage } from "./HostsPage";
import { HubDetailsPage } from "./HubDetailsPage";
import { HubHome } from "./HubHome";
import { HubsPage } from "./HubsPage";
import { PluginsPage } from "./PluginsPage";
import { ProvidersPage } from "./ProvidersPage";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider, useClosesOnHubChange } from "./hubSheetContext";
import { useHubUpdates } from "./hubUpdates";

const HubStack = createNativeStackNavigator<HubRoutes>();

const ADD_HUB_TITLES: Record<HubRoutes["AddHub"]["how"], string> = {
	scan: "Scan pairing code",
	paste: "Paste pairing link",
	address: "Enter the address",
};

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile } = useConnection();
	const hubId = activeProfile?.id ?? "";
	const { state, canUseConnection, renderClient } = useRetainedScreenConnection(hubId);
	const { palette } = useColors();
	const close = useCallback(() => navigation.goBack(), [navigation]);
	// With no hub selected, the Board under this sheet is stale: first run
	// becomes the whole stack. (React Navigation 7's navigate would push a
	// second Hubs screen, inside this sheet, rather than go back to the first.)
	const leave = useCallback(() => navigation.reset({ index: 0, routes: [{ name: "Hubs" }] }), [navigation]);
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
			<HubStack.Navigator screenOptions={sheetStackOptions(palette)}>
				<HubStack.Screen
					name="HubHome"
					component={HubHome}
					initialParams={{ hubId }}
					options={{
						title: activeProfile.name,
						headerRight: () => <HeaderButton label="Done" strong onPress={close} />,
					}}
				/>
				<HubStack.Screen name="Display" component={DisplayPage} options={{ title: "Display" }} />
				<HubStack.Screen name="DetailLevel" component={DetailLevelPage} options={{ title: "Default detail level" }} />
				<HubStack.Screen name="Alerts" component={AlertsPage} options={{ title: "In-app alerts" }} />
				<HubStack.Screen name="Hosts" component={HostsPage} options={{ title: "Hosts" }} />
				<HubStack.Screen
					name="HostDetail"
					component={HostDetailPage}
					options={({ route }) => ({ title: route.params.name })}
				/>
				{/* The page sets its own title and its Cancel and Save. */}
				<HubStack.Screen name="HostEdit" component={HostEditPage} />
				<HubStack.Screen name="Providers" component={ProvidersPage} options={{ title: "Providers" }} />
				<HubStack.Screen name="Plugins" component={PluginsPage} options={{ title: "Plugins" }} />
				<HubStack.Screen name="Hubs" component={HubsPage} options={{ title: "Hubs" }} />
				<HubStack.Screen
					name="AddHub"
					component={AddHubPage}
					options={({ route }) => ({ title: ADD_HUB_TITLES[route.params.how] })}
				/>
				<HubStack.Screen name="HubDetails" component={HubDetailsPage} />
				<HubStack.Screen
					name="KeybindingPreferences"
					component={KeybindingPreferencesScreen}
					options={{ title: "Keyboard shortcuts" }}
				/>
				<HubStack.Screen name="LaunchSettings" component={LaunchSettingsScreen} options={{ title: "Launch defaults" }} />
				<HubStack.Screen name="HubSettings" component={HubSettingsScreen} options={{ title: "Hub settings" }} />
			</HubStack.Navigator>
		</HubSheetProvider>
	);
}
