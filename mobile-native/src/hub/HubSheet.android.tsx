// Android's native Board opens its administration pages inside this sheet.
import { randomUUID } from "expo-crypto";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { isReady } from "../connectionDisplay";
import { useHubFleet } from "../hosts/useHubFleet";
import { HubSettingsScreen } from "../HubSettingsScreen";
import { KeybindingPreferencesScreen } from "../KeybindingPreferencesScreen";
import { LaunchSettingsScreen } from "../LaunchSettingsScreen";
import { useRetainedScreenConnection } from "../retainedScreen";
import { HeaderButton } from "../sheet/HeaderButton";
import type { Routes } from "../screens";
import { AlertsPage } from "./AlertsPage";
import { DetailLevelPage } from "./DetailLevelPage";
import { DisplayPage } from "./DisplayPage";
import { HostDetailPage } from "./HostDetailPage";
import { HostEditPage } from "./HostEditPage";
import { HostsPage } from "./HostsPage";
import { HubHome } from "./HubHome";
import { MarketplacePage } from "./MarketplacePage";
import { OwnHostPage } from "./OwnHostPage";
import { PluginsPage } from "./PluginsPage";
import { PluginsScreenSlotProvider } from "./hubScreenSlot";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { ProvidersPage } from "./ProvidersPage";
import { ProvidersScreenSlotProvider } from "./hubScreenSlot";
import { type HubSheetContextValue, HubSheetProvider } from "./hubSheetContext";
import { useHubUpdates } from "./hubUpdates";
import { HubStack, savedHubScreens, useHubSheetNavigation } from "./savedHubScreens";

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile, hubId, close, sheetStackOptions } = useHubSheetNavigation(navigation);
	const { state, canUseConnection, renderClient } = useRetainedScreenConnection(hubId);
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
			<PluginsScreenSlotProvider>
				<ProvidersScreenSlotProvider>
					<HubStack.Navigator screenOptions={sheetStackOptions}>
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
						<HubStack.Screen
							name="DetailLevel"
							component={DetailLevelPage}
							options={{ title: "Default detail level" }}
						/>
						<HubStack.Screen name="Alerts" component={AlertsPage} options={{ title: "In-app alerts" }} />
						<HubStack.Screen name="Hosts" component={HostsPage} options={{ title: "Hosts" }} />
						<HubStack.Screen
							name="HostDetail"
							component={HostDetailPage}
							options={({ route }) => ({ title: route.params.name })}
						/>
						<HubStack.Screen name="OwnHost" component={OwnHostPage} options={{ title: activeProfile.name }} />
						{/* The page sets its own title and its Cancel and Save. */}
						<HubStack.Screen name="HostEdit" component={HostEditPage} />
						<HubStack.Screen name="Providers" component={ProvidersPage} options={{ title: "Providers" }} />
						<HubStack.Screen
							name="ProviderDetail"
							component={ProviderDetailPage}
							options={({ route }) => ({ title: route.params.name })}
						/>
						<HubStack.Screen name="Plugins" component={PluginsPage} options={{ title: "Plugins" }} />
						<HubStack.Screen
							name="Marketplace"
							component={MarketplacePage}
							options={({ route }) => ({ title: route.params.name })}
						/>
						{savedHubScreens()}
						<HubStack.Screen
							name="KeybindingPreferences"
							component={KeybindingPreferencesScreen}
							options={{ title: "Web keyboard shortcuts" }}
						/>
						<HubStack.Screen
							name="LaunchSettings"
							component={LaunchSettingsScreen}
							options={{ title: "Launch defaults" }}
						/>
						<HubStack.Screen name="HubSettings" component={HubSettingsScreen} options={{ title: "Hub settings" }} />
					</HubStack.Navigator>
				</ProvidersScreenSlotProvider>
			</PluginsScreenSlotProvider>
		</HubSheetProvider>
	);
}
