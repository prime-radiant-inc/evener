import {
	createNavigationContainerRef,
	DarkTheme,
	DefaultTheme,
	NavigationContainer,
	type NavigationState,
	StackActions,
} from "@react-navigation/native";
import { createNativeStackNavigator } from "@react-navigation/native-stack";
import { StatusBar } from "expo-status-bar";
import { useEffect, useRef, useState } from "react";
import { ActivityIndicator, useColorScheme, View } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { AlertBannerHost } from "./src/alerts/AlertBannerHost";
import { AlertsProvider } from "./src/alerts/AlertsProvider";
import { useReportRoutes } from "./src/alerts/alertsContext";
import { ConnectionProvider, useConnection } from "./src/ConnectionProvider";
import { HubSheet } from "./src/hub/HubSheet";
import { FirstRunScreen } from "./src/hubs/FirstRunScreen";
import { locationForRoute, phoneLaunchStack, routeToSave } from "./src/location";
import { locations } from "./src/nativeLocation";
import { stackRoutes } from "./src/navigationRoutes";
import { useRootStackOptions } from "./src/rootStack";
import type { Routes } from "./src/screens";
import type { SharedPayload, WorkspaceShare } from "./src/share/sharePayload";
import { ShareTargetSheet } from "./src/share/ShareTargetSheet";
import { useShareIntake } from "./src/share/useShareIntake";
import { ErrorMessage, useColors } from "./src/ui";
import { WorkspaceScreen } from "./src/WorkspaceScreen";

const Stack = createNativeStackNavigator<Routes>();
const navigationRef = createNavigationContainerRef<Routes>();

export default function App() {
	return (
		<SafeAreaProvider>
			<ConnectionProvider>
				<AlertsProvider>
					<Navigation />
				</AlertsProvider>
			</ConnectionProvider>
		</SafeAreaProvider>
	);
}

function Navigation() {
	const { loading, initialLocation, activeProfile, restorationError, state: connectionState } = useConnection();
	const hubId = activeProfile?.id;
	const [state, setState] = useState<NavigationState>();
	const [saveError, setSaveError] = useState<string | null>(null);
	const [offer, setOffer] = useState<SharedPayload | null>(null);
	const [share, setShare] = useState<WorkspaceShare>();
	const picker = useRef<string | null>(null);
	const intake = useShareIntake({ ready: connectionState === "ready", onShare: setOffer });
	const reportRoutes = useReportRoutes();
	const colors = useColors();
	const rootStackOptions = useRootStackOptions();
	const dark = useColorScheme() === "dark";

	useEffect(() => {
		if (!offer || !hubId || !state?.routes.some((route) => route.name === "Workspace")) return;
		picker.current = offer.id;
		navigationRef.navigate("ShareTarget", { item: offer });
		setOffer(null);
	}, [offer, hubId, state]);

	const route = state && routeToSave(state);
	useEffect(() => {
		if (!route || loading) return;
		try {
			locations.save(locationForRoute(route, hubId ?? null));
			setSaveError(null);
		} catch {
			setSaveError("This screen could not be saved for reopening after restart.");
		}
	}, [route, hubId, loading]);

	useEffect(() => {
		if (!share || hubId === share.hubId) return;
		setShare(undefined);
		intake.release();
	}, [share, hubId, intake]);

	function finishShare(id: string) {
		picker.current = null;
		setShare(undefined);
		intake.forget(id);
		void intake.advance(id);
	}

	function report(next: NavigationState | undefined) {
		setState(next);
		if (!next) return;
		reportRoutes(stackRoutes(next));
		// A navigation dispatch may render before its state-change notification.
		// Release a share only when leaving the destination it actually reached.
		if (share && route?.name === "Workspace" && (route.params as Routes["Workspace"])?.path === share.path) {
			const front = routeToSave(next);
			if (front?.name !== "Workspace" || (front.params as Routes["Workspace"])?.path !== share.path) {
				setShare(undefined);
				intake.release();
			}
		}
	}

	if (loading) {
		return (
			<View style={{ flex: 1, justifyContent: "center", backgroundColor: colors.background }}>
				<ActivityIndicator accessibilityLabel="Loading saved hubs" />
			</View>
		);
	}
	return (
		<View style={{ flex: 1, backgroundColor: colors.background }}>
			<ErrorMessage message={saveError || restorationError} />
			<NavigationContainer
				ref={navigationRef}
				initialState={phoneLaunchStack(initialLocation)}
				onReady={() => report(navigationRef.getRootState())}
				onStateChange={report}
				theme={dark ? DarkTheme : DefaultTheme}
			>
				<StatusBar style={dark ? "light" : "dark"} />
				<Stack.Navigator screenOptions={rootStackOptions}>
					<Stack.Screen name="Hubs" component={FirstRunScreen} options={{ headerShown: false }} />
					<Stack.Screen name="Workspace" options={{ headerShown: false, gestureEnabled: false }}>
						{(props) => (
							<WorkspaceScreen
								{...props}
								share={share}
								onSharePlaced={(id) => {
									if (share?.id === id && share.hubId === hubId) finishShare(id);
								}}
							/>
						)}
					</Stack.Screen>
					<Stack.Screen name="Hub" component={HubSheet} options={{ presentation: "modal", headerShown: false }} />
					<Stack.Screen name="ShareTarget" options={{ presentation: "modal", title: "Share to Evener" }}>
						{({ navigation, route: target }) => (
							<ShareTargetSheet
								navigation={navigation}
								route={target}
								onChoose={(destinationHub, path, item) => {
									if (!hubId || destinationHub !== hubId || picker.current !== item.id) return;
									picker.current = null;
									navigation.dispatch(StackActions.popTo("Workspace", { hubId, path }));
									setShare({ ...item, hubId, path });
								}}
								onDiscarded={(item) => finishShare(item.id)}
								onClosed={() => {
									if (picker.current === target.params.item.id) {
										picker.current = null;
										intake.release();
									}
								}}
							/>
						)}
					</Stack.Screen>
				</Stack.Navigator>
				<AlertBannerHost navigation={navigationRef} />
			</NavigationContainer>
		</View>
	);
}
