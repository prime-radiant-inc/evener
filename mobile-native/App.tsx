import {
	DarkTheme,
	DefaultTheme,
	NavigationContainer,
	type NavigationState,
} from "@react-navigation/native";
import { createNativeStackNavigator } from "@react-navigation/native-stack";
import { StatusBar } from "expo-status-bar";
import { useEffect, useState } from "react";
import { ActivityIndicator, useColorScheme, View } from "react-native";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { BoardScreen } from "./src/board/BoardScreen";
import { RowMenuSheet } from "./src/board/RowMenu";
import { ConnectionProvider, useConnection } from "./src/ConnectionProvider";
import { ForkScreen } from "./src/ForkScreen";
import { HubSettingsScreen } from "./src/HubSettingsScreen";
import { KeybindingPreferencesScreen } from "./src/KeybindingPreferencesScreen";
import { LaunchSettingsScreen } from "./src/LaunchSettingsScreen";
import { locationForRoute, restoredStack, routeToSave } from "./src/location";
import { NativePreferencesProvider } from "./src/NativePreferencesProvider";
import { NewSessionScreen } from "./src/NewSessionScreen";
import { locations } from "./src/nativeLocation";
import { PinAssignmentScreen } from "./src/PinAssignmentScreen";
import { PinSectionEditorScreen } from "./src/PinSectionEditorScreen";
import {
	PinnedSectionScreen,
	PinSectionsScreen,
} from "./src/PinSectionsScreen";
import { PluginsScreen } from "./src/PluginsScreen";
import {
	ProjectScreen,
	ProjectsScreen,
	SessionLocationScreen,
} from "./src/ProjectsScreen";
import { ProvidersScreen } from "./src/ProvidersScreen";
import { SessionDeletionScreen } from "./src/SessionDeletionScreen";
import {
	ConversationScreen,
	HubsScreen,
	type Routes,
} from "./src/screens";
import { SHEET_ROUTES } from "./src/sheet/sheetRoutes";
import { TasksSheet } from "./src/TasksSheet";
import { TranscriptPreferencesScreen } from "./src/TranscriptPreferencesScreen";
import { ErrorMessage, useColors } from "./src/ui";

const Stack = createNativeStackNavigator<Routes>();

export default function App() {
	// Gesture handlers recognize touches only inside this view, so it wraps
	// everything, and it fills the screen.
	return (
		<GestureHandlerRootView style={{ flex: 1 }}>
			<SafeAreaProvider>
				<ConnectionProvider>
					<NativePreferencesProvider>
						<Navigation />
					</NativePreferencesProvider>
				</ConnectionProvider>
			</SafeAreaProvider>
		</GestureHandlerRootView>
	);
}
function Navigation() {
	const { loading, initialLocation, activeProfile, restorationError } =
		useConnection();
	const [state, setState] = useState<NavigationState>();
	const [saveError, setSaveError] = useState<string | null>(null);
	useEffect(() => {
		if (!state || loading) return;
		const route = routeToSave(state);
		if (!route) return;
		try {
			locations.save(locationForRoute(route, activeProfile?.id ?? null));
			setSaveError(null);
		} catch {
			setSaveError(
				"This screen could not be saved for reopening after restart.",
			);
		}
	}, [state, activeProfile?.id, loading]);
	const colors = useColors();
	const dark = useColorScheme() === "dark";
	if (loading)
		return (
			<View
				style={{
					flex: 1,
					justifyContent: "center",
					backgroundColor: colors.background,
				}}
			>
				<ActivityIndicator accessibilityLabel="Loading saved hubs" />
			</View>
		);
	return (
		<View style={{ flex: 1, backgroundColor: colors.background }}>
			<ErrorMessage message={saveError || restorationError} />
			<NavigationContainer
				initialState={restoredStack(initialLocation)}
				onStateChange={setState}
				theme={dark ? DarkTheme : DefaultTheme}
			>
				<StatusBar style={dark ? "light" : "dark"} />
				<Stack.Navigator
					screenOptions={{
						headerStyle: { backgroundColor: colors.background },
						headerTintColor: colors.accent,
						headerTitleStyle: { color: colors.text },
						headerShadowVisible: false,
						headerBackButtonDisplayMode: "minimal",
						contentStyle: { backgroundColor: colors.background },
					}}
				>
					<Stack.Screen
						name="Hubs"
						component={HubsScreen}
						options={{ title: "Evener · Hubs" }}
					/>
					<Stack.Screen name="Sessions" component={BoardScreen} />
					<Stack.Screen
						name="SessionDeletion"
						component={SessionDeletionScreen}
						options={{ title: "Delete saved session" }}
					/>
					<Stack.Screen
						name="Fork"
						component={ForkScreen}
						options={{ title: "Fork conversation" }}
					/>
					<Stack.Screen name="Projects" component={ProjectsScreen} />
					<Stack.Screen
						name="PinSections"
						component={PinSectionsScreen}
						options={{ title: "Pinned sections" }}
					/>
					<Stack.Screen
						name="PinnedSection"
						component={PinnedSectionScreen}
						options={{ title: "Pinned section" }}
					/>
					<Stack.Screen
						name="PinSectionEditor"
						component={PinSectionEditorScreen}
						options={{ title: "Manage section" }}
					/>
					<Stack.Screen
						name="PinAssignment"
						component={PinAssignmentScreen}
						options={{ title: "Pin session" }}
					/>
					<Stack.Screen
						name="LaunchSettings"
						component={LaunchSettingsScreen}
						options={({ route }) => ({
							title:
								route.params.projectCwd === undefined
									? "Launch defaults"
									: "Project launch settings",
						})}
					/>
					<Stack.Screen
						name="KeybindingPreferences"
						component={KeybindingPreferencesScreen}
						options={{ title: "Keyboard shortcuts" }}
					/>
					<Stack.Screen
						name="TranscriptPreferences"
						component={TranscriptPreferencesScreen}
						options={{ title: "Transcript display" }}
					/>
					<Stack.Screen name="Providers" component={ProvidersScreen} />
					<Stack.Screen name="Plugins" component={PluginsScreen} />
					<Stack.Screen
						name="HubSettings"
						component={HubSettingsScreen}
						options={{ title: "Hub settings" }}
					/>
					<Stack.Screen
						name="SessionLocation"
						component={SessionLocationScreen}
						options={({ route }) => ({ title: route.params.location.title })}
					/>
					<Stack.Screen
						name="Project"
						component={ProjectScreen}
						options={({ route }) => ({
							title: route.params.title || "Project",
						})}
					/>
					<Stack.Screen
						name="NewSession"
						component={NewSessionScreen}
						options={{ title: "New session" }}
					/>
					<Stack.Screen
						name="Conversation"
						component={ConversationScreen}
						options={({ route }) => ({
							title: route.params.title || "Conversation",
						})}
					/>
					<Stack.Group
						screenOptions={{
							contentStyle: { backgroundColor: colors.palette.canvas },
						}}
					>
						<Stack.Screen
							name="TasksSheet"
							component={TasksSheet}
							options={SHEET_ROUTES.TasksSheet}
						/>
						<Stack.Screen
							name="RowMenuSheet"
							component={RowMenuSheet}
							options={SHEET_ROUTES.RowMenuSheet}
						/>
					</Stack.Group>
				</Stack.Navigator>
			</NavigationContainer>
		</View>
	);
}
