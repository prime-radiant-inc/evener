// The Hub (spec 12): a large sheet from the Board's hub button that holds its
// own stack, so each page pushes inside the sheet (ruling 1). Pages read the
// hub, its client and the connection's readiness from the sheet's context
// (hubSheetContext.tsx).
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Pressable, Text } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { isReady } from "../connectionDisplay";
import { useRetainedScreenConnection } from "../retainedScreen";
import type { Routes } from "../screens";
import { useColors } from "../ui";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider, useClosesOnHubChange } from "./hubSheetContext";
import { createPhoneHubUpdates, createReadiness } from "./hubUpdates";

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
	// One update controller per client, checked each time the connection is
	// ready: on opening, and after every reconnect, which is also how a
	// restarted hub is noticed (hubUpdates.ts).
	const [readiness] = useState(createReadiness);
	const updates = useMemo(() => createPhoneHubUpdates(renderClient, readiness), [renderClient, readiness]);
	useEffect(() => () => updates.dispose(), [updates]);
	useEffect(() => {
		readiness.set(ready);
		if (ready) void updates.controller.runCheck();
	}, [ready, readiness, updates]);
	const value = useMemo<HubSheetContextValue>(
		() => ({
			hubId,
			hubName: activeProfile?.name ?? "",
			client: renderClient,
			ready,
			canUseConnection,
			updates: updates.controller,
		}),
		[hubId, activeProfile?.name, renderClient, ready, canUseConnection, updates],
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
						headerRight: () => (
							<Pressable accessibilityRole="button" accessibilityLabel="Done" hitSlop={8} onPress={close}>
								<Text style={{ color: palette.accentInk, fontSize: 17, fontWeight: "600" }}>Done</Text>
							</Pressable>
						),
					}}
				/>
			</HubStack.Navigator>
		</HubSheetProvider>
	);
}
