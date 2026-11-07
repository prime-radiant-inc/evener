import { useNavigation } from "@react-navigation/native";
import {
	createNativeStackNavigator,
	type NativeStackNavigationProp,
	type NativeStackScreenProps,
} from "@react-navigation/native-stack";
import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { Text, useWindowDimensions, View } from "react-native";
import { BoardScreen } from "../board/BoardScreen";
import { ForkScreen } from "../ForkScreen";
import { PinAssignmentScreen } from "../PinAssignmentScreen";
import { SessionLocationScreen } from "../ProjectsScreen";
import { QueueSheet } from "../QueueSheet";
import { CommentSheet } from "../reader/CommentSheet";
import { CommentsSheet } from "../reader/CommentsSheet";
import { FilesSheet } from "../reader/FilesSheet";
import { OutlineSheet } from "../reader/OutlineSheet";
import { ReaderScreen } from "../reader/ReaderScreen";
import { ReviewSheet } from "../reader/ReviewSheet";
import { useRootStackOptions } from "../rootStack";
import { ConversationScreen, type Routes } from "../screens";
import { SessionDeletionScreen } from "../SessionDeletionScreen";
import { CommandsSheet } from "../session/CommandsSheet";
import { ModelSheet } from "../session/ModelSheet";
import { NotesSheet } from "../session/NotesSheet";
import { SessionInfoSheet } from "../session/SessionInfoSheet";
import { replaceAnimation } from "../session/titleSwipe";
import { SHEET_ROUTES } from "../sheet/sheetRoutes";
import { ActivityScreen } from "../subagents/ActivityScreen";
import { ShellJobScreen } from "../subagents/ShellJobScreen";
import { StopSubagentSheet } from "../subagents/StopSubagentSheet";
import { SubagentScreen } from "../subagents/SubagentScreen";
import { TasksSheet } from "../TasksSheet";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** The right pane's routes: the conversation and everything it can open,
 * plus the empty route the pane starts on. */
type DetailRoutes = Routes & { DuoDetail: undefined };
type DetailNavigation = NativeStackNavigationProp<DetailRoutes>;

const DetailStack = createNativeStackNavigator<DetailRoutes>();
const DetailNavigationContext = createContext<((navigation: DetailNavigation | null) => void) | null>(null);

/** The Duo's inner display: the Board stays on the left and the conversation
 * opens beside it. The Board remains the root stack's focused Sessions
 * screen, so its live, search and scroll state keeps running. */
export function DuoSplit({ navigation, route }: NativeStackScreenProps<Routes, "Sessions">) {
	const { width } = useWindowDimensions();
	const { palette } = useColors();
	const rootOptions = useRootStackOptions();
	const [detailNavigation, setDetailNavigation] = useState<DetailNavigation | null>(null);
	const pending = useRef<Routes["Conversation"] | null>(null);
	const openConversation = useCallback(
		(params: Routes["Conversation"]) => {
			const detail = detailNavigation;
			if (!detail) {
				pending.current = params;
				return;
			}
			// Replacing the whole detail stack keeps conversations from
			// stacking and clears a Reader or Activity the old one opened.
			detail.reset({
				index: 1,
				routes: [{ name: "DuoDetail" }, { name: "Conversation", params }],
			});
		},
		[detailNavigation],
	);
	useEffect(() => {
		if (!detailNavigation || !pending.current) return;
		const params = pending.current;
		pending.current = null;
		openConversation(params);
	}, [detailNavigation, openConversation]);
	const sidebarWidth = Math.min(380, Math.max(280, Math.round(width * 0.36)));
	return (
		<View style={{ flex: 1, flexDirection: "row", backgroundColor: palette.page }}>
			<View style={{ width: sidebarWidth }}>
				<BoardScreen
					navigation={navigation}
					route={route}
					duoEmbedded
					duoWidth={sidebarWidth}
					onOpenConversation={openConversation}
				/>
			</View>
			<View style={{ width: 1, backgroundColor: palette.edge }} />
			<View style={{ flex: 1 }}>
				<DetailNavigationContext.Provider value={setDetailNavigation}>
					<DetailStack.Navigator initialRouteName="DuoDetail" screenOptions={rootOptions}>
						<DetailStack.Screen name="DuoDetail" component={DuoDetail} options={{ headerShown: false }} />
						<DetailStack.Screen
							name="Conversation"
							component={ConversationScreen}
							options={({ route }) => ({
								title: route.params.title || "Conversation",
								animationTypeForReplace: replaceAnimation(route.params),
							})}
						/>
						<DetailStack.Screen name="Reader" component={ReaderScreen} options={{ title: "" }} />
						<DetailStack.Screen name="Activity" component={ActivityScreen} options={{ title: "" }} />
						<DetailStack.Screen
							name="Subagent"
							component={SubagentScreen}
							options={({ route }) => ({ title: route.params.title || "Subagent" })}
						/>
						<DetailStack.Screen name="ShellJob" component={ShellJobScreen} options={{ title: "Shell job" }} />
						<DetailStack.Screen name="Fork" component={ForkScreen} options={{ title: "Fork conversation" }} />
						<DetailStack.Screen
							name="SessionDeletion"
							component={SessionDeletionScreen}
							options={{ title: "Delete saved session" }}
						/>
						<DetailStack.Screen
							name="PinAssignment"
							component={PinAssignmentScreen}
							options={{ title: "Pin to category" }}
						/>
						<DetailStack.Screen
							name="SessionLocation"
							component={SessionLocationScreen}
							options={({ route }) => ({ title: route.params.location.title })}
						/>
						<DetailStack.Group screenOptions={{ contentStyle: { backgroundColor: palette.canvas } }}>
							<DetailStack.Screen name="TasksSheet" component={TasksSheet} options={SHEET_ROUTES.TasksSheet} />
							<DetailStack.Screen name="NotesSheet" component={NotesSheet} options={SHEET_ROUTES.NotesSheet} />
							<DetailStack.Screen name="QueueSheet" component={QueueSheet} options={SHEET_ROUTES.QueueSheet} />
							<DetailStack.Screen name="OutlineSheet" component={OutlineSheet} options={SHEET_ROUTES.OutlineSheet} />
							<DetailStack.Screen name="CommentSheet" component={CommentSheet} options={SHEET_ROUTES.CommentSheet} />
							<DetailStack.Screen name="CommentsSheet" component={CommentsSheet} options={SHEET_ROUTES.CommentsSheet} />
							<DetailStack.Screen name="ReviewSheet" component={ReviewSheet} options={SHEET_ROUTES.ReviewSheet} />
							<DetailStack.Screen
								name="SessionInfoSheet"
								component={SessionInfoSheet}
								options={SHEET_ROUTES.SessionInfoSheet}
							/>
							<DetailStack.Screen name="ModelSheet" component={ModelSheet} options={SHEET_ROUTES.ModelSheet} />
							<DetailStack.Screen name="CommandsSheet" component={CommandsSheet} options={SHEET_ROUTES.CommandsSheet} />
							<DetailStack.Screen name="FilesSheet" component={FilesSheet} options={SHEET_ROUTES.FilesSheet} />
							<DetailStack.Screen
								name="StopSubagentSheet"
								component={StopSubagentSheet}
								options={SHEET_ROUTES.StopSubagentSheet}
							/>
						</DetailStack.Group>
					</DetailStack.Navigator>
				</DetailNavigationContext.Provider>
			</View>
		</View>
	);
}

/** The pane's first route. It hands the split its navigation object so a Board
 * tap can open a conversation without the split reaching into the navigator's
 * internals. */
function DuoDetail() {
	const setDetailNavigation = useContext(DetailNavigationContext);
	const navigation = useNavigation<DetailNavigation>();
	useEffect(() => {
		setDetailNavigation?.(navigation);
		return () => setDetailNavigation?.(null);
	}, [navigation, setDetailNavigation]);
	return <DuoEmpty />;
}

/** What the detail pane says before a conversation is chosen. */
function DuoEmpty() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				flex: 1,
				alignItems: "center",
				justifyContent: "center",
				paddingHorizontal: 32,
				backgroundColor: palette.page,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, lineHeight: 24 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Select a conversation
			</Text>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					marginTop: 6,
					fontSize: 15 * scale,
					lineHeight: 21 * scale,
					color: palette.inkLow,
					textAlign: "center",
				}}
			>
				Choose a session from the Board.
			</Text>
		</View>
	);
}
