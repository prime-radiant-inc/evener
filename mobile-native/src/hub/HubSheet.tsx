import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import type { Routes } from "../screens";
import { HubStack, savedHubScreens, useHubSheetNavigation } from "./savedHubScreens";

export function HubSheet({ navigation }: NativeStackScreenProps<Routes, "Hub">) {
	const { activeProfile, close, sheetStackOptions } = useHubSheetNavigation(navigation);
	if (!activeProfile) return null;
	return <HubStack.Navigator screenOptions={sheetStackOptions}>{savedHubScreens(close)}</HubStack.Navigator>;
}
