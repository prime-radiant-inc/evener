import {
	createNavigationContainerRef,
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
import { KeyboardProvider } from "react-native-keyboard-controller";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { AlertBannerHost } from "./src/alerts/AlertBannerHost";
import { AlertsProvider } from "./src/alerts/AlertsProvider";
import { useReportRoutes } from "./src/alerts/alertsContext";
import { BoardScreen } from "./src/board/BoardScreen";
import { RowMenuSheet } from "./src/board/RowMenu";
import { ConnectionProvider, useConnection } from "./src/ConnectionProvider";
import { DisplayProvider } from "./src/display/displayContext";
import { displayPreferences, followAppearanceChoice } from "./src/display/nativeDisplay";
import { HubSheet } from "./src/hub/HubSheet";
import { FirstRunScreen } from "./src/hubs/FirstRunScreen";
import { ForkScreen } from "./src/ForkScreen";
import { locationForRoute, restoredStack, routeToSave } from "./src/location";
import { NativePreferencesProvider } from "./src/NativePreferencesProvider";
import { locations } from "./src/nativeLocation";
import { NewSessionSheet } from "./src/newSession/NewSessionSheet";
import { outboxFlush } from "./src/outbox/nativeOutboxFlush";
import { PinAssignmentScreen } from "./src/PinAssignmentScreen";
import { PinSectionEditorScreen } from "./src/PinSectionEditorScreen";
import { useRootStackOptions } from "./src/rootStack";
import {
	PinnedSectionScreen,
	PinSectionsScreen,
} from "./src/PinSectionsScreen";
import {
	ProjectScreen,
	ProjectsScreen,
	SessionLocationScreen,
} from "./src/ProjectsScreen";
import { SessionDeletionScreen } from "./src/SessionDeletionScreen";
import { ConversationScreen, type Routes } from "./src/screens";
import { ModelSheet } from "./src/session/ModelSheet";
import { CommandsSheet } from "./src/session/CommandsSheet";
import { NotesSheet } from "./src/session/NotesSheet";
import { SessionInfoSheet } from "./src/session/SessionInfoSheet";
import { SHEET_ROUTES } from "./src/sheet/sheetRoutes";
import { QueueSheet } from "./src/QueueSheet";
import { CommentSheet } from "./src/reader/CommentSheet";
import { CommentsSheet } from "./src/reader/CommentsSheet";
import { FilesSheet } from "./src/reader/FilesSheet";
import { OutlineSheet } from "./src/reader/OutlineSheet";
import { ReviewSheet } from "./src/reader/ReviewSheet";
import { StopSubagentSheet } from "./src/subagents/StopSubagentSheet";
import { SubagentScreen } from "./src/subagents/SubagentScreen";
import { SubagentsScreen } from "./src/subagents/SubagentsScreen";
import { ReaderScreen } from "./src/reader/ReaderScreen";
import { replaceAnimation } from "./src/session/titleSwipe";
import { TasksSheet } from "./src/TasksSheet";
import { ErrorMessage, useColors } from "./src/ui";

const Stack = createNativeStackNavigator<Routes>();
const navigationRef = createNavigationContainerRef<Routes>();

/** The root stack's routes up to the focused one, as alerts read them. */
function stackRoutes(state: NavigationState) {
	return state.routes
		.slice(0, state.index + 1)
		.map((route) => ({ name: route.name, params: route.params }));
}

// Runs before the first render, so the first frame already has the
// appearance chosen in Display (spec 12), native chrome included.
followAppearanceChoice();

export default function App() {
	// Gesture handlers recognize touches only inside this view, so it wraps
	// everything, and it fills the screen. The keyboard controller reports the
	// keyboard's frames to what moves with it (the Session's composer).
	return (
		<GestureHandlerRootView style={{ flex: 1 }}>
			<KeyboardProvider>
				<SafeAreaProvider>
					<DisplayProvider value={displayPreferences}>
						<ConnectionProvider>
							<NativePreferencesProvider>
								<AlertsProvider>
									<Navigation />
								</AlertsProvider>
							</NativePreferencesProvider>
						</ConnectionProvider>
					</DisplayProvider>
				</SafeAreaProvider>
			</KeyboardProvider>
		</GestureHandlerRootView>
	);
}
function Navigation() {
	const {
		loading,
		initialLocation,
		activeProfile,
		restorationError,
		client,
		state: connectionState,
	} = useConnection();
	const [state, setState] = useState<NavigationState>();
	// What no open session is sending goes on a ready connection (ruling 17).
	useEffect(() => {
		outboxFlush.bind(
			activeProfile?.id ?? null,
			connectionState === "ready" ? client : null,
		);
	}, [activeProfile?.id, connectionState, client]);
	const [saveError, setSaveError] = useState<string | null>(null);
	// In-app alerts follow what is on screen (spec 13.3).
	const reportRoutes = useReportRoutes();
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
	const rootStackOptions = useRootStackOptions();
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
				ref={navigationRef}
				initialState={restoredStack(initialLocation)}
				onReady={() => {
					const root = navigationRef.getRootState();
					if (root) reportRoutes(stackRoutes(root));
				}}
				onStateChange={(next) => {
					setState(next);
					if (next) reportRoutes(stackRoutes(next));
				}}
				theme={dark ? DarkTheme : DefaultTheme}
			>
				<StatusBar style={dark ? "light" : "dark"} />
				<Stack.Navigator screenOptions={rootStackOptions}>
					<Stack.Screen
						name="Hubs"
						component={FirstRunScreen}
						options={{ headerShown: false }}
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
						name="Hub"
						component={HubSheet}
						options={{ presentation: "modal", headerShown: false }}
					/>
					<Stack.Screen
						name="NewSession"
						component={NewSessionSheet}
						options={{ presentation: "modal", headerShown: false }}
					/>
					<Stack.Screen
						name="Conversation"
						component={ConversationScreen}
						options={({ route }) => ({
							title: route.params.title || "Conversation",
							animationTypeForReplace: replaceAnimation(route.params),
						})}
					/>
					<Stack.Screen
						name="Reader"
						component={ReaderScreen}
						options={{ title: "" }}
					/>
					<Stack.Screen
						name="Subagents"
						component={SubagentsScreen}
						options={{ title: "" }}
					/>
					<Stack.Screen
						name="Subagent"
						component={SubagentScreen}
						options={({ route }) => ({ title: route.params.title || "Subagent" })}
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
							name="NotesSheet"
							component={NotesSheet}
							options={SHEET_ROUTES.NotesSheet}
						/>
						<Stack.Screen
							name="RowMenuSheet"
							component={RowMenuSheet}
							options={SHEET_ROUTES.RowMenuSheet}
						/>
						<Stack.Screen
							name="QueueSheet"
							component={QueueSheet}
							options={SHEET_ROUTES.QueueSheet}
						/>
						<Stack.Screen
							name="OutlineSheet"
							component={OutlineSheet}
							options={SHEET_ROUTES.OutlineSheet}
						/>
						<Stack.Screen
							name="CommentSheet"
							component={CommentSheet}
							options={SHEET_ROUTES.CommentSheet}
						/>
						<Stack.Screen
							name="CommentsSheet"
							component={CommentsSheet}
							options={SHEET_ROUTES.CommentsSheet}
						/>
						<Stack.Screen
							name="ReviewSheet"
							component={ReviewSheet}
							options={SHEET_ROUTES.ReviewSheet}
						/>
						<Stack.Screen
							name="SessionInfoSheet"
							component={SessionInfoSheet}
							options={SHEET_ROUTES.SessionInfoSheet}
						/>
						<Stack.Screen
							name="ModelSheet"
							component={ModelSheet}
							options={SHEET_ROUTES.ModelSheet}
						/>
						<Stack.Screen
							name="CommandsSheet"
							component={CommandsSheet}
							options={SHEET_ROUTES.CommandsSheet}
						/>
						<Stack.Screen
							name="FilesSheet"
							component={FilesSheet}
							options={SHEET_ROUTES.FilesSheet}
						/>
						<Stack.Screen
							name="StopSubagentSheet"
							component={StopSubagentSheet}
							options={SHEET_ROUTES.StopSubagentSheet}
						/>
					</Stack.Group>
				</Stack.Navigator>
				<AlertBannerHost navigation={navigationRef} />
			</NavigationContainer>
		</View>
	);
}
