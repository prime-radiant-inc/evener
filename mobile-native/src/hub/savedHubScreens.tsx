import { createNativeStackNavigator, type NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback } from "react";
import { useConnection } from "../ConnectionProvider";
import type { Routes } from "../screens";
import { HeaderButton } from "../sheet/HeaderButton";
import { useSheetStackOptions } from "../sheet/sheetStack";
import { AddHubPage } from "./AddHubPage";
import { HubDetailsPage } from "./HubDetailsPage";
import { HubsPage } from "./HubsPage";
import { type HubRoutes, useClosesOnHubChange } from "./hubSheetContext";

export const HubStack = createNativeStackNavigator<HubRoutes>();

const ADD_HUB_TITLES: Record<HubRoutes["AddHub"]["how"], string> = {
	scan: "Scan pairing code",
	paste: "Paste pairing link",
	address: "Enter the address",
};

export function useHubSheetNavigation(navigation: NativeStackScreenProps<Routes, "Hub">["navigation"]) {
	const { activeProfile } = useConnection();
	const hubId = activeProfile?.id ?? "";
	const sheetStackOptions = useSheetStackOptions();
	const close = useCallback(() => navigation.goBack(), [navigation]);
	// Removing the selected hub replaces the old workspace with first run.
	const leave = useCallback(() => navigation.reset({ index: 0, routes: [{ name: "Hubs" }] }), [navigation]);
	useClosesOnHubChange(hubId, close, leave);
	return { activeProfile, hubId, close, sheetStackOptions };
}

export function savedHubScreens(close?: () => void) {
	return (
		<HubStack.Group>
			<HubStack.Screen
				name="Hubs"
				component={HubsPage}
				options={{
					title: "Hubs",
					headerRight: close ? () => <HeaderButton label="Done" strong onPress={close} /> : undefined,
				}}
			/>
			<HubStack.Screen
				name="AddHub"
				component={AddHubPage}
				options={({ route }) => ({ title: ADD_HUB_TITLES[route.params.how] })}
			/>
			<HubStack.Screen name="HubDetails" component={HubDetailsPage} />
		</HubStack.Group>
	);
}
