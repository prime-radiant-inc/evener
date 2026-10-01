import { useFocusEffect, useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
	type ReactElement,
	type ReactNode,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { AccessibilityInfo, ActivityIndicator, Alert, FlatList, Platform, Pressable, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { ArchiveParams, NavigationProjectSummary, NavigationSessionSummary } from "@evener/appwire-client";
import { useConnection } from "./ConnectionProvider";
import { organizationJournal } from "./nativeOrganization";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";
import { ArchivedPages, archivedListStoreFor } from "./archivedPages";
import { NavigationPages, type PageSource, updating } from "./navigationPages";
import { revealNavigationRow } from "./navigationReveal";
import {
	controllerOwnedProject,
	type OrganizationObservation,
	projectName,
	readOrganizationNavigation,
} from "./organizationNavigation";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
import { sessionSubagentChip, sessionSubagentChipLabel } from "./board/BoardRow";

const noSnapshot = () => null;
const noSubscription = () => () => {};

function FilterTab({ label, selected, onPress }: { label: string; selected: boolean; onPress: () => void }) {
	const colors = useColors();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ selected }}
			onPress={onPress}
			style={{
				minHeight: 48,
				paddingHorizontal: 12,
				paddingVertical: 10,
				borderRadius: 12,
				backgroundColor: selected ? colors.surface : "transparent",
				borderBottomWidth: selected ? 2 : 0,
				borderColor: colors.accent,
			}}
		>
			<Copy>{label}</Copy>
		</Pressable>
	);
}

/** Why the page read after an organize change can't confirm it, or null when
 * it does: the read must have landed cleanly, and a navigation page must come
 * from the generation the change was observed in. An archived list has no
 * navigation version, so only its read is checked. */
export function pageConfirmationError(
	pages: PageSource<unknown>,
	observation: Pick<OrganizationObservation, "generationId"> | null,
): string | null {
	const page = pages.getSnapshot();
	if (!page.loaded || page.loading || page.stale || page.error) return "The current navigation could not be confirmed.";
	if (observation && pages.navigationVersioned && pages.getResourceVersion()?.generationId !== observation.generationId)
		return "The hub restarted during the check.";
	return null;
}

export function PageList<T>({
	header,
	pages,
	ready,
	rowKey,
	title,
	detail,
	open,
	empty,
	organization,
	organizationHubId,
	revealRef,
	chip,
	chipLabel,
}: {
	header?: ReactElement;
	organizationHubId?: string;
	revealRef?: string;
	/** A row's trailing chip, beside its title (the subagent count). */
	chip?: (row: T) => ReactNode;
	/** The chip's text, for the row's accessibilityLabel: a Pressable override
	 * hides the chip's own text from VoiceOver. Undefined when there is none. */
	chipLabel?: (row: T) => string | undefined;
	pages: PageSource<T>;
	ready: boolean;
	rowKey: (row: T) => string;
	title: (row: T) => string;
	detail: (row: T) => string;
	open: (row: T) => void;
	empty: string;
	organization: (row: T) => {
		target: Omit<ArchiveParams, "archived">;
		archived: boolean;
		favorite?: boolean;
	} | null;
}) {
	const colors = useColors();
	const state = useSyncExternalStore(pages.subscribe, pages.getSnapshot);
	const { client, activeProfile } = useConnection();
	const focused = useIsFocused();
	const binding = useMemo(
		() => ({ client, pages, ready, focused, organizationHubId }),
		[client, pages, ready, focused, organizationHubId],
	);
	const current = useRef(binding);
	current.current = binding;
	const [review, setReview] = useState<{
		owner: typeof binding;
		checkpoint: NavigationActionCheckpoint;
		observation: OrganizationObservation;
	} | null>(null);
	const actions = useMemo(() => {
		if (!client || !ready || !focused) return null;
		let previous: {
			checkpoint: NavigationActionCheckpoint;
			observation: OrganizationObservation;
		} | null = null;
		let checkedPage: ReturnType<typeof pages.getSnapshot> | null = null;
		const isCurrent = () => current.current === binding;
		const refresh = async (checkpoint?: NavigationActionCheckpoint, confirmReceipt = false, acceptCurrent = false) => {
			setReview(null);
			const observation = checkpoint
				? await readOrganizationNavigation(client, checkpoint, isCurrent, confirmReceipt)
				: null;
			if (confirmReceipt && checkpoint?.receipt) await pages.refreshAfter(checkpoint.receipt);
			else await pages.refresh();
			const page = pages.getSnapshot();
			if (!isCurrent()) throw Error("The current navigation could not be confirmed.");
			const unconfirmed = pageConfirmationError(pages, observation);
			if (unconfirmed) throw Error(unconfirmed);
			const same = previous !== null && JSON.stringify(previous) === JSON.stringify({ checkpoint, observation });
			previous = checkpoint && observation ? { checkpoint, observation } : null;
			setReview(checkpoint && observation ? { owner: binding, checkpoint, observation } : null);
			if (observation && !observation.settled && !(acceptCurrent && same))
				throw Error("Review the current organization before continuing.");
			checkedPage = page;
		};
		return new NavigationActions(
			client,
			(_receipt, checkpoint) => refresh(checkpoint, true),
			isCurrent,
			(checkpoint, acceptCurrent) => refresh(checkpoint, false, acceptCurrent),
			organizationHubId ? organizationJournal(organizationHubId) : undefined,
			() => {
				if (!isCurrent() || pages.getSnapshot() !== checkedPage)
					throw Error("Navigation changed before the check finished.");
			},
		);
	}, [client, pages, ready, focused, binding, organizationHubId]);
	useEffect(() => () => actions?.dispose(), [actions]);
	const actionState = useSyncExternalStore(actions?.subscribe ?? noSubscription, actions?.getSnapshot ?? noSnapshot);

	useEffect(() => pages.watch(), [pages]);
	const list = useRef<FlatList<T>>(null);
	const rowKeyNow = useRef(rowKey);
	rowKeyNow.current = rowKey;
	const [revealError, setRevealError] = useState<string | null>(null);
	const [revealRequest, setRevealRequest] = useState(0);
	const [loadingMore, setLoadingMore] = useState(false);
	// iOS ignores accessibilityLiveRegion, so announce the longer list's arrival
	// there; Android reads the polite region on its own, and announcing too would
	// say it twice. loadingMore starts false, so the first render stays quiet (#2903).
	useEffect(() => {
		if (loadingMore && Platform.OS === "ios") AccessibilityInfo.announceForAccessibility("Loading more…");
	}, [loadingMore]);
	const revealEpoch = useRef(revealRequest);
	revealEpoch.current = revealRequest;
	function refreshList() {
		if (revealRef) setRevealRequest((value) => value + 1);
		void actions?.reconcile();
	}
	const requestMore = useCallback(
		(allowError = false) => {
			const current = pages.getSnapshot();
			if (
				!ready ||
				!focused ||
				!current.loaded ||
				current.loading ||
				(!allowError && current.error) ||
				current.remaining <= 0
			)
				return;
			setLoadingMore(true);
			void pages.more().finally(() => setLoadingMore(false));
		},
		[focused, pages, ready],
	);
	const loadMore = useCallback(() => requestMore(), [requestMore]);
	const retryMore = useCallback(() => requestMore(true), [requestMore]);
	const [revealed, setRevealed] = useState<PageSource<T> | null>(null);
	const scrollAttempt = useRef(0);
	const scrollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	useEffect(
		() => () => {
			if (scrollTimer.current) clearTimeout(scrollTimer.current);
		},
		[],
	);
	useFocusEffect(
		useCallback(() => {
			let active = true;
			if (ready) pages.resume();
			if (ready && revealRef) {
				setRevealError(null);
				setRevealed(null);
				void revealNavigationRow(
					pages,
					revealRef,
					rowKeyNow.current,
					() => active && revealEpoch.current === revealRequest,
				)
					.then((found) => {
						if (!active || !found) return;
						scrollAttempt.current = 0;
						setRevealed(pages);
					})
					.catch((error) => {
						if (active) setRevealError(error instanceof Error ? error.message : "Could not locate this session.");
					});
			} else if (ready && !pages.getSnapshot().loaded) void pages.refresh();
			return () => {
				active = false;
				if (scrollTimer.current) clearTimeout(scrollTimer.current);
				pages.cancel();
			};
		}, [pages, ready, revealRef, revealRequest]),
	);
	const targetIndex = revealRef ? state.rows.findIndex((row) => rowKey(row) === revealRef) : -1;
	useEffect(() => {
		if (revealed === pages && targetIndex >= 0)
			list.current?.scrollToIndex({
				index: targetIndex,
				animated: false,
				viewPosition: 0.3,
			});
	}, [revealed, pages, targetIndex]);
	return (
		<>
			{revealError ? (
				<View style={{ paddingHorizontal: 16, gap: 8 }}>
					<ErrorMessage message={revealError} />
					<Action disabled={!ready || state.loading} onPress={refreshList}>
						Locate again
					</Action>
				</View>
			) : null}
			{updating(state) ? (
				<View style={{ paddingHorizontal: 16, paddingTop: 8 }}>
					<Copy muted>Updating…</Copy>
				</View>
			) : null}
			{state.error ? (
				<View style={{ paddingHorizontal: 16, paddingTop: 8 }}>
					<ErrorMessage message={state.error} />
					<Action
						disabled={!ready || state.loading}
						onPress={() => {
							refreshList();
						}}
					>
						Try again
					</Action>
				</View>
			) : null}
			<FlatList
				ListHeaderComponent={
					<>
						{header}
						{actions ? (
							<OrganizationStatus actions={actions} review={review?.owner === binding ? review : null} />
						) : null}
					</>
				}
				ref={list}
				onScrollToIndexFailed={({ index, averageItemLength }) => {
					list.current?.scrollToOffset({
						offset: averageItemLength * index,
						animated: false,
					});
					if (scrollAttempt.current++ < 3) {
						if (scrollTimer.current) clearTimeout(scrollTimer.current);
						scrollTimer.current = setTimeout(
							() =>
								list.current?.scrollToIndex({
									index,
									animated: false,
									viewPosition: 0.3,
								}),
							100,
						);
					}
				}}
				data={state.rows}
				keyExtractor={rowKey}
				onEndReachedThreshold={0.5}
				onEndReached={loadMore}
				contentContainerStyle={styles.padded}
				ListEmptyComponent={
					state.loading ? (
						<ActivityIndicator accessibilityLabel="Loading list" />
					) : (
						<Copy muted>{ready ? empty : "Connect to this hub to load the list."}</Copy>
					)
				}
				ListFooterComponent={
					<View style={{ gap: 8 }}>
						{loadingMore ? (
							<View accessibilityLiveRegion="polite" style={{ alignItems: "center", paddingVertical: 8 }}>
								<ActivityIndicator accessibilityLabel="Loading more results" />
								<Copy muted>Loading more…</Copy>
							</View>
						) : state.remaining > 0 ? (
							<Action disabled={!ready || !focused || state.loading} onPress={state.error ? retryMore : loadMore}>
								{state.error ? "Retry loading more" : `Load more · ${state.remaining} remaining`}
							</Action>
						) : null}
					</View>
				}
				renderItem={({ item }) => (
					<View
						style={[
							styles.row,
							{
								backgroundColor: rowKey(item) === revealRef ? colors.surface : "transparent",
								borderBottomWidth: 0.5,
								borderColor: colors.border,
							},
						]}
					>
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={[`Open ${title(item)}`, chipLabel?.(item)].filter(Boolean).join(", ")}
							accessibilityState={{ selected: rowKey(item) === revealRef }}
							disabled={!ready}
							onPress={() => open(item)}
							style={{ flex: 1, paddingVertical: 13, minHeight: 68, gap: 4 }}
						>
							<View style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
								<View style={{ flex: 1, minWidth: 0 }}>
									<Copy>{title(item)}</Copy>
								</View>
								{chip?.(item)}
							</View>
							<Copy muted>{detail(item)}</Copy>
						</Pressable>
						{actions && organization(item) ? (
							<Action
								tone="quiet"
								label={`More actions for ${title(item)}`}
								disabled={
									!ready ||
									state.loading ||
									!!actionState?.pending ||
									!!actionState?.uncertain ||
									!!actionState?.storageUnavailable
								}
								onPress={() => {
									const value = organization(item);
									const actionState = actions.getSnapshot();
									if (!value || actionState.pending || actionState.uncertain || actionState.storageUnavailable) return;
									const invoke = (operation: () => void) => {
										if (current.current === binding) operation();
									};
									Alert.alert(
										title(item),
										`${activeProfile?.name ?? "Hub"} · Organize without deleting history or stopping work.`,
										[
											...(value.favorite === undefined
												? []
												: [
														{
															text: value.favorite ? "Remove from pinned" : "Add to pinned",
															onPress: () =>
																invoke(() => {
																	void actions.favorite(value.target.id, !value.favorite);
																}),
														},
													]),
											{
												text: value.archived ? "Unarchive" : "Archive",
												onPress: () =>
													invoke(() => {
														void actions.archive(value.target, !value.archived);
													}),
											},
											{ text: "Cancel", style: "cancel" },
										],
									);
								}}
							>
								···
							</Action>
						) : null}
					</View>
				)}
			/>
		</>
	);
}
function OrganizationStatus({
	actions,
	review,
}: {
	actions: NavigationActions;
	review: {
		checkpoint: NavigationActionCheckpoint;
		observation: OrganizationObservation;
	} | null;
}) {
	const state = useSyncExternalStore(actions.subscribe, actions.getSnapshot);
	if (!state.pending && !state.error) return null;
	const observation =
		review && !review.observation.settled && JSON.stringify(review.checkpoint) === JSON.stringify(state.recovery)
			? review.observation
			: null;
	const descriptions = {
		archived: "Archived",
		current: "Current sessions",
		recent: "Recent sessions",
		projects: "Projects",
		pinned: "Pinned",
		unpinned: "Not pinned",
	};
	return (
		<View style={{ gap: 8, paddingVertical: 8 }}>
			{state.pending ? <ActivityIndicator accessibilityLabel="Updating organization" /> : null}
			<ErrorMessage message={state.pending || observation ? null : state.error} />
			{observation ? (
				<>
					<Copy>
						{review?.checkpoint.receipt
							? "The current state differs from the requested change."
							: "The previous request is still unconfirmed."}
					</Copy>
					<Copy>
						{observation.title} · {descriptions[observation.state]}
					</Copy>
				</>
			) : null}
			{state.uncertain ? (
				<>
					<Action
						disabled={state.pending}
						onPress={() => {
							void actions.reconcile();
						}}
					>
						Check again
					</Action>
					{observation && !observation.settled && review ? (
						<>
							<Copy muted>
								The current state is shown above. Continue to keep it without sending the previous request again.
							</Copy>
							<Action
								disabled={state.pending}
								onPress={() => {
									void actions.keepOrganizationState(review.checkpoint);
								}}
							>
								Continue with current state
							</Action>
						</>
					) : null}
				</>
			) : null}
		</View>
	);
}
const projectKey = (row: NavigationProjectSummary) => row.key;
const sessionRef = (row: NavigationSessionSummary) => row.ref;

export function ProjectsScreen({ route, navigation }: NativeStackScreenProps<Routes, "Projects">) {
	const { client, activeProfile, state } = useConnection();
	const colors = useColors();
	const archived = route.params.archived ?? false;
	const belongs = activeProfile?.id === route.params.hubId;
	const pages = useMemo(
		() =>
			client && belongs
				? new NavigationPages<NavigationProjectSummary>(
						client,
						{
							resource: "catalog",
							catalog: archived ? "archived_projects" : "projects",
						},
						"projects",
						projectKey,
					)
				: null,
		[client, belongs, archived],
	);
	return (
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
			<View style={{ paddingHorizontal: 20, gap: 8 }}>
				<Copy muted>{belongs ? activeProfile?.name : "Disconnected hub"}</Copy>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<FilterTab label="Projects" selected={!archived} onPress={() => navigation.setParams({ archived: false })} />
					<FilterTab
						label="Archived projects"
						selected={archived}
						onPress={() => navigation.setParams({ archived: true })}
					/>
				</View>
			</View>
			{pages ? (
				<PageList
					organizationHubId={route.params.hubId}
					pages={pages}
					ready={state === "ready"}
					rowKey={projectKey}
					organization={(row) =>
						row.key === "no-project" || !controllerOwnedProject(row.sources)
							? null
							: {
									target: {
										kind: "project",
										id: row.key,
										workingDir: row.working_dir,
									},
									archived: row.is_archived ?? archived,
									favorite: row.favorite ?? false,
								}
					}
					title={projectName}
					detail={(row) =>
						`${row.favorite ? "Pinned · " : ""}${row.session_count} sessions${row.working_dir ? ` · ${row.working_dir}` : ""}`
					}
					empty={archived ? "No archived projects." : "No projects on this hub yet."}
					open={(row) =>
						navigation.navigate("Project", {
							hubId: route.params.hubId,
							projectKey: row.key,
							title: row.name,
							archived,
						})
					}
				/>
			) : (
				<Copy muted>Connect to this hub to browse projects.</Copy>
			)}
		</SafeAreaView>
	);
}
export function ProjectScreen({ route, navigation }: NativeStackScreenProps<Routes, "Project">) {
	const { client, activeProfile, state } = useConnection();
	const colors = useColors();
	const tier = route.params.tier ?? "current";
	const belongs = activeProfile?.id === route.params.hubId;
	const catalog = route.params.archived ? "archived_projects" : "projects";
	const pages = useMemo(
		() =>
			!client || !belongs
				? null
				: // Navigation serves no archived rows; the archived list does.
					tier === "archived"
					? new ArchivedPages(archivedListStoreFor(client), catalog, route.params.projectKey)
					: new NavigationPages<NavigationSessionSummary>(
							client,
							{
								resource: "project_page",
								projectKey: route.params.projectKey,
								tier,
							},
							"sessions",
							sessionRef,
						),
		[client, belongs, catalog, route.params.projectKey, tier],
	);
	return (
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
			<View style={{ paddingHorizontal: 20, gap: 8 }}>
				<Copy muted>{belongs ? activeProfile?.name : "Disconnected hub"}</Copy>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					{(["current", "recent", "archived"] as const).map((value) => (
						<FilterTab
							key={value}
							selected={tier === value}
							onPress={() => navigation.setParams({ tier: value })}
							label={value === "current" ? "Current" : value === "recent" ? "Recent" : "Archived"}
						/>
					))}
				</View>
			</View>
			{pages ? (
				<PageList
					organizationHubId={route.params.hubId}
					pages={pages}
					ready={state === "ready"}
					rowKey={sessionRef}
					organization={(row) =>
						row.host_id !== "local" ||
						row.ref !== `local:${row.session_id}` ||
						["subagent", "fork", "cluster"].includes(row.kind)
							? null
							: {
									target: { kind: "session", id: row.session_id },
									archived: tier === "archived",
								}
					}
					chip={sessionSubagentChip}
					chipLabel={sessionSubagentChipLabel}
					title={(row) => row.title || "Untitled session"}
					detail={(row) =>
						`${row.ask_pending || row.state === "awaiting" ? "Needs you" : row.state}${row.branch ? ` · ${row.branch}` : ""}`
					}
					empty={`No ${tier} sessions in this project.`}
					open={(row) =>
						navigation.navigate("Conversation", {
							hubId: route.params.hubId,
							ref: row.ref,
							title: row.title,
						})
					}
				/>
			) : (
				<Copy muted>Connect to this hub to browse sessions.</Copy>
			)}
		</SafeAreaView>
	);
}

export function SessionLocationScreen({ route, navigation }: NativeStackScreenProps<Routes, "SessionLocation">) {
	const { client, activeProfile, state } = useConnection();
	const colors = useColors();
	const belongs = activeProfile?.id === route.params.hubId;
	const { location } = route.params;
	const pages = useMemo(
		() =>
			client && belongs
				? new NavigationPages<NavigationSessionSummary>(client, location.params, "sessions", sessionRef)
				: null,
		[client, belongs, location],
	);
	return (
		<SafeAreaView edges={["bottom", "left", "right"]} style={[styles.fill, { backgroundColor: colors.background }]}>
			<View style={{ paddingHorizontal: 20, gap: 8 }}>
				<Copy muted>
					{belongs ? activeProfile?.name : "Disconnected hub"}
					{location.params.tier ? ` · ${location.params.tier}` : ""}
				</Copy>
				{(location.revealRef ?? location.ref) !== location.ref ? (
					<Copy muted>Showing the row that owns this session</Copy>
				) : null}
			</View>
			{pages ? (
				<PageList
					pages={pages}
					ready={state === "ready"}
					revealRef={location.revealRef ?? location.ref}
					rowKey={sessionRef}
					chip={sessionSubagentChip}
					chipLabel={sessionSubagentChipLabel}
					title={(row) => row.title || "Untitled session"}
					detail={(row) => (row.ask_pending || row.state === "awaiting" ? "Needs you" : row.state)}
					empty="No sessions in this location."
					organization={() => null}
					open={(row) =>
						navigation.navigate("Conversation", {
							hubId: route.params.hubId,
							ref: row.ref,
							title: row.title,
						})
					}
				/>
			) : (
				<Copy muted>Connect to this hub to locate the session.</Copy>
			)}
		</SafeAreaView>
	);
}
