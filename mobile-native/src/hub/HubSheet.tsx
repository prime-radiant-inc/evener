// The Hub (spec 12): a large sheet from the Board's hub button that holds its
// own stack, so each page pushes inside the sheet (ruling 1). Pages read the
// hub, its client and the connection's readiness from the sheet's context
// (hubSheetContext.tsx).
import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useMemo } from "react";
import { Pressable, Text } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { isReady } from "../connectionDisplay";
import { useRetainedScreenConnection } from "../retainedScreen";
import type { Routes } from "../screens";
import { useColors } from "../ui";
import { HubHome } from "./HubHome";
import { type HubRoutes, type HubSheetContextValue, HubSheetProvider, useClosesOnHubChange } from "./hubSheetContext";

const HubStack = createNativeStackNavigator<HubRoutes>();

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile } = useConnection();
	const hubId = activeProfile?.id ?? "";
	const { state, canUseConnection, renderClient } = useRetainedScreenConnection(hubId);
	const { palette } = useColors();
	const close = useCallback(() => navigation.goBack(), [navigation]);
	const leave = useCallback(() => navigation.navigate("Hubs"), [navigation]);
	useClosesOnHubChange(hubId, close, leave);
	const value = useMemo<HubSheetContextValue>(
		() => ({
			hubId,
			hubName: activeProfile?.name ?? "",
			client: renderClient,
			ready: isReady(state),
			canUseConnection,
		}),
		[hubId, activeProfile?.name, renderClient, state, canUseConnection],
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
