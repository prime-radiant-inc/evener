import {
	type ReactElement,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import {
	ActivityIndicator,
	FlatList,
	Pressable,
	Text,
	View,
	type ViewToken,
} from "react-native";
import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { navigationTree } from "./navigationTree";
import {
	createProjectBrowserController,
	type ProjectSessionTier,
} from "./projectBrowser";
import { Action, Copy, useColors } from "./ui";

type BrowserRow =
	| {
			kind: "project";
			key: string;
			project: NavigationProjectSummary;
			expanded: boolean;
	  }
	| {
			kind: "session";
			key: string;
			projectKey: string;
			session: NavigationSessionSummary;
			depth: number;
	  }
	| {
			kind: "page";
			key: string;
			projectKey: string;
			tier: ProjectSessionTier;
			loading: boolean;
			error: string | null;
			stale: boolean;
	  }
	| { kind: "empty"; key: string }
	| { kind: "limited"; key: string };
const sessionKey = (session: NavigationSessionSummary) => session.ref;
const sessionChildren = (session: NavigationSessionSummary) =>
	session.children ?? [];

/** One scroll surface owns projects, their sessions, and page boundaries. */
export function ProjectSessionsList({
	client,
	ready,
	focused,
	header,
	openSession,
	openProject,
}: {
	client: ConversationClientLike;
	ready: boolean;
	focused: boolean;
	header: ReactElement;
	openSession(session: NavigationSessionSummary): void;
	openProject(project: NavigationProjectSummary): void;
}) {
	const colors = useColors();
	const browser = useMemo(
		() => createProjectBrowserController(client),
		[client],
	);
	const state = useSyncExternalStore(browser.subscribe, browser.getSnapshot);
	const connection = useRef({
		browser,
		initialized: false,
		needsRefresh: false,
	});
	const [refreshing, setRefreshing] = useState(false);
	const [expandedChildren, setExpandedChildren] = useState(new Set<string>());
	useEffect(() => () => browser.dispose(), [browser]);
	useEffect(() => {
		if (connection.current.browser !== browser) {
			connection.current = { browser, initialized: false, needsRefresh: false };
		}
		if (!ready) {
			connection.current.needsRefresh = connection.current.initialized;
			return;
		}
		if (!focused) return;
		if (!connection.current.initialized) {
			connection.current.initialized = true;
			void browser.initialLoad();
		} else if (connection.current.needsRefresh) {
			connection.current.needsRefresh = false;
			void browser.refresh();
		}
	}, [browser, ready, focused]);
	const rows = useMemo(() => {
		const result: BrowserRow[] = [];
		for (const project of state.projects.rows) {
			const group = state.groups.find(
				(value) => value.project.key === project.key,
			);
			result.push({
				kind: "project",
				key: `project:${project.key}`,
				project,
				expanded: group?.expanded ?? false,
			});
			if (!group?.expanded) continue;
			const seen = new Set<string>();
			let staleNoticeAdded = false;
			for (const tier of ["current", "recent"] as const) {
				const page = group[tier];
				for (const { item, depth } of navigationTree(
					page.rows,
					sessionKey,
					sessionChildren,
					expandedChildren,
				)) {
					if (seen.has(item.ref)) continue;
					seen.add(item.ref);
					result.push({
						kind: "session",
						key: `session:${project.key}:${item.ref}`,
						projectKey: project.key,
						session: item,
						depth,
					});
				}
				if (
					page.loading ||
					(page.error && !page.stale) ||
					(page.remaining > 0 && !page.stale)
				) {
					result.push({
						kind: "page",
						key: `page:${project.key}:${tier}`,
						projectKey: project.key,
						tier,
						loading: page.loading,
						error: page.error,
						stale: false,
					});
				}
				if (page.stale && !staleNoticeAdded) {
					staleNoticeAdded = true;
					result.push({
						kind: "page",
						key: `page:${project.key}:stale`,
						projectKey: project.key,
						tier,
						loading: false,
						error: null,
						stale: true,
					});
				}
			}
			if (group.current.truncated || group.recent.truncated) {
				result.push({ kind: "limited", key: `limited:${project.key}` });
			}
			if (
				!group.sessions.length &&
				group.current.loaded &&
				group.recent.loaded &&
				!group.current.error &&
				!group.recent.error
			) {
				result.push({ kind: "empty", key: `empty:${project.key}` });
			}
		}
		return result;
	}, [state, expandedChildren]);
	const current = useRef({ browser, ready, focused });
	current.current = { browser, ready, focused };
	const viewability = useRef({ itemVisiblePercentThreshold: 1 });
	const visibleRowsChanged = useRef(
		({ viewableItems }: { viewableItems: ViewToken<BrowserRow>[] }) => {
			const access = current.current;
			if (!access.ready || !access.focused) return;
			for (const token of viewableItems) {
				if (token.isViewable && token.item.kind === "page") {
					void access.browser.loadMoreSessions(
						token.item.projectKey,
						token.item.tier,
					);
				}
			}
		},
	);
	async function refresh() {
		if (!ready || refreshing) return;
		setRefreshing(true);
		try {
			await browser.refresh();
		} finally {
			setRefreshing(false);
		}
	}
	function chevron(expanded: boolean) {
		return (
			<View
				accessible={false}
				style={{
					width: 7,
					height: 7,
					borderRightWidth: 1.5,
					borderBottomWidth: 1.5,
					borderColor: colors.secondary,
					transform: [{ rotate: expanded ? "45deg" : "-45deg" }],
					marginRight: 6,
				}}
			/>
		);
	}
	return (
		<FlatList
			data={rows}
			keyExtractor={(row) => row.key}
			keyboardShouldPersistTaps="handled"
			keyboardDismissMode="on-drag"
			ListHeaderComponent={header}
			contentContainerStyle={{ paddingBottom: 28 }}
			refreshing={refreshing}
			onRefresh={() => {
				void refresh();
			}}
			onEndReachedThreshold={0.5}
			onEndReached={() => {
				if (ready && focused) void browser.loadMoreProjects();
			}}
			viewabilityConfig={viewability.current}
			onViewableItemsChanged={visibleRowsChanged.current}
			ListEmptyComponent={
				state.projects.loading ? (
					<View
						accessibilityLabel="Loading projects"
						style={{ paddingHorizontal: 20, gap: 24 }}
					>
						{[0, 1, 2].map((index) => (
							<View key={index} style={{ gap: 12, paddingVertical: 16 }}>
								<View
									style={{
										width: "42%",
										height: 18,
										borderRadius: 4,
										backgroundColor: colors.surface,
									}}
								/>
								<View
									style={{
										width: "80%",
										height: 14,
										borderRadius: 4,
										backgroundColor: colors.surface,
									}}
								/>
							</View>
						))}
					</View>
				) : (
					<View style={{ padding: 20, gap: 12 }}>
						<Copy>
							{state.projects.error
								? "Could not load projects."
								: ready
									? "Your projects will appear here."
									: "Connect to this hub to see your projects."}
						</Copy>
						{state.projects.error ? (
							<Action
								onPress={() => {
									void browser.retry();
								}}
								disabled={!ready}
							>
								Retry
							</Action>
						) : ready ? (
							<Copy muted>Create a session to start working in a project.</Copy>
						) : null}
					</View>
				)
			}
			ListFooterComponent={
				state.projects.rows.length ? (
					<View style={{ paddingHorizontal: 20, paddingTop: 12, gap: 8 }}>
						{state.projects.loading && !refreshing ? (
							<ActivityIndicator accessibilityLabel="Loading more projects" />
						) : null}
						{state.projects.stale ? (
							<>
								<Copy muted>Projects have changed.</Copy>
								<Action
									disabled={!ready || state.projects.loading}
									onPress={() => {
										void refresh();
									}}
								>
									Refresh projects
								</Action>
							</>
						) : state.projects.error ? (
							<>
								<Copy muted>Could not load more projects.</Copy>
								<Action
									disabled={!ready || state.projects.loading}
									onPress={() => {
										void browser.retry();
									}}
								>
									Retry
								</Action>
							</>
						) : null}
					</View>
				) : null
			}
			renderItem={({ item }) => {
				if (item.kind === "project") {
					const attentionCount = item.project.rollup_attn ?? 0;
					const attentionLabel =
						attentionCount > 0
							? attentionCount === 1
								? "1 needs attention"
								: `${attentionCount} need attention`
							: null;
					return (
						<View
							style={{
								marginTop: 16,
								marginHorizontal: 20,
								borderBottomColor: colors.border,
								borderBottomWidth: 0.5,
								flexDirection: "row",
								alignItems: "center",
							}}
						>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`${item.project.name || "Project"}${attentionLabel ? `, ${attentionLabel}` : ""}`}
								accessibilityHint={item.project.working_dir}
								accessibilityState={{
									expanded: item.expanded,
									disabled: !ready,
								}}
								disabled={!ready}
								onPress={() => {
									void browser.toggle(item.project.key);
								}}
								style={({ pressed }) => ({
									flex: 1,
									minHeight: 52,
									flexDirection: "row",
									alignItems: "center",
									gap: 12,
									opacity: pressed ? 0.6 : 1,
								})}
							>
								{chevron(item.expanded)}
								<View style={{ flex: 1, gap: 2 }}>
									<Text
										style={{
											fontSize: 19,
											fontWeight: "600",
											color: colors.text,
										}}
										numberOfLines={2}
									>
										{item.project.name || "Untitled project"}
									</Text>
									{attentionLabel ? (
										<Text
											style={{
												fontSize: 13,
												lineHeight: 19,
												color: colors.accent,
											}}
										>
											{attentionLabel}
										</Text>
									) : null}
								</View>
								<Copy muted>{item.project.session_count}</Copy>
							</Pressable>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`Open ${item.project.name} project details`}
								disabled={!ready}
								onPress={() => openProject(item.project)}
								style={({ pressed }) => ({
									minWidth: 44,
									minHeight: 44,
									flexDirection: "row",
									alignItems: "center",
									justifyContent: "center",
									gap: 3,
									opacity: pressed ? 0.6 : 1,
								})}
							>
								<View
									style={{
										width: 3,
										height: 3,
										borderRadius: 2,
										backgroundColor: colors.secondary,
									}}
								/>
								<View
									style={{
										width: 3,
										height: 3,
										borderRadius: 2,
										backgroundColor: colors.secondary,
									}}
								/>
								<View
									style={{
										width: 3,
										height: 3,
										borderRadius: 2,
										backgroundColor: colors.secondary,
									}}
								/>
							</Pressable>
						</View>
					);
				}
				if (item.kind === "limited")
					return (
						<View style={{ paddingHorizontal: 40, paddingVertical: 12 }}>
							<Copy muted>
								The hub limited this project’s results. Some related sessions
								may be missing.
							</Copy>
						</View>
					);
				if (item.kind === "empty")
					return (
						<View style={{ paddingHorizontal: 40, paddingVertical: 16 }}>
							<Copy muted>No current or recent sessions in this project.</Copy>
						</View>
					);
				if (item.kind === "page")
					return (
						<View style={{ paddingHorizontal: 40, paddingVertical: 8 }}>
							{item.loading ? (
								<ActivityIndicator accessibilityLabel="Loading sessions" />
							) : item.stale ? (
								<>
									<Copy muted>This project has updates.</Copy>
									<Action
										disabled={!ready}
										onPress={() => {
											void refresh();
										}}
									>
										Refresh
									</Action>
								</>
							) : item.error ? (
								<>
									<Copy muted>Could not load more sessions.</Copy>
									<Action
										disabled={!ready}
										onPress={() => {
											void browser.retry(item.projectKey, item.tier);
										}}
									>
										Retry
									</Action>
								</>
							) : (
								<Action
									tone="quiet"
									disabled={!ready}
									onPress={() => {
										void browser.loadMoreSessions(item.projectKey, item.tier);
									}}
								>
									Load more sessions
								</Action>
							)}
						</View>
					);
				const session = item.session;
				const omitted =
					(session.omitted_descendants ?? 0) + (session.more_subagents ?? 0);
				const waiting = session.ask_pending === true;
				const status = waiting
					? "Question waiting"
					: session.state === "active"
						? "Working"
						: session.state === "errored"
							? "Failed"
							: session.state === "warning"
								? "Needs attention"
								: "";
				const detail = [status, session.branch].filter(Boolean).join(" · ");
				return (
					<View
						style={{
							marginLeft: 40 + Math.min(item.depth, 2) * 12,
							marginRight: 20,
							borderBottomColor: colors.border,
							borderBottomWidth: 0.5,
							flexDirection: "row",
							alignItems: "center",
						}}
					>
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={`Open ${session.title || "Untitled session"}`}
							accessibilityHint={detail}
							disabled={!ready}
							onPress={() => openSession(session)}
							style={({ pressed }) => ({
								flex: 1,
								minHeight: 68,
								paddingVertical: 12,
								gap: 4,
								opacity: pressed ? 0.6 : 1,
							})}
						>
							<Text
								style={{ fontSize: 17, lineHeight: 23, color: colors.text }}
								numberOfLines={2}
							>
								{session.title || "Untitled session"}
							</Text>
							{detail ? <Copy muted>{detail}</Copy> : null}
							{omitted > 0 ? (
								<Copy
									muted
								>{`${omitted} related ${omitted === 1 ? "session is" : "sessions are"} not included in this list.`}</Copy>
							) : null}
						</Pressable>
						{session.children?.length ? (
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`Related sessions for ${session.title}`}
								accessibilityState={{
									expanded: expandedChildren.has(session.ref),
								}}
								onPress={() =>
									setExpandedChildren((before) => {
										const next = new Set(before);
										if (next.has(session.ref)) next.delete(session.ref);
										else next.add(session.ref);
										return next;
									})
								}
								style={{
									minWidth: 44,
									minHeight: 44,
									alignItems: "center",
									justifyContent: "center",
								}}
							>
								{chevron(expandedChildren.has(session.ref))}
							</Pressable>
						) : null}
					</View>
				);
			}}
		/>
	);
}
