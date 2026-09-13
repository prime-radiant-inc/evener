import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { NavigationSessionSummary } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { NavigationPages } from "./navigationPages";
import { PinCatalogList } from "./PinCatalogList";
import { PageList } from "./ProjectsScreen";
import type { Routes } from "./screens";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
import { usePinNavigation } from "./usePinNavigation";

export function PinSectionsScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "PinSections">) {
	const pin = usePinNavigation(route.params.hubId);
	const colors = useColors();
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{!pin.belongs ? (
				<Copy>
					This hub is no longer selected. Return to Hubs to reconnect.
				</Copy>
			) : (
				<PinCatalogList
					sections={pin.page?.rows ?? []}
					hubName={pin.activeProfile?.name ?? "Hub"}
					connected={pin.ready}
					loaded={pin.confirmed}
					loading={pin.page?.loading ?? false}
					stale={pin.page?.stale ?? false}
					remaining={pin.page?.remaining ?? 0}
					pending={pin.action?.pending ?? false}
					uncertain={
						!!pin.action?.uncertain || !!pin.action?.storageUnavailable
					}
					error={pin.action?.error ?? pin.page?.error ?? null}
					refresh={() => {
						if (pin.ready) void pin.actions?.reconcile();
						else pin.retry();
					}}
					more={() => {
						if (
							pin.isCurrent() &&
							pin.confirmed &&
							!pin.action?.pending &&
							!pin.action?.uncertain
						)
							void pin.pages?.more();
					}}
					open={(section) =>
						navigation.navigate("PinnedSection", {
							hubId: route.params.hubId,
							sectionId: section.id,
							title: section.name,
						})
					}
				/>
			)}
		</SafeAreaView>
	);
}

const sessionKey = (row: NavigationSessionSummary) => row.ref;
export function PinnedSectionScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "PinnedSection">) {
	const { hubId, sectionId, title } = route.params;
	const pin = usePinNavigation(hubId, undefined, sectionId);
	const colors = useColors();
	const pages = useMemo(
		() =>
			pin.client && pin.belongs
				? new NavigationPages<NavigationSessionSummary>(
						pin.client,
						{ resource: "pin_section", sectionId },
						"sessions",
						sessionKey,
					)
				: null,
		[pin.client, pin.belongs, sectionId],
	);
	const section = pin.observed?.section;
	const ready =
		pin.ready &&
		pin.focused &&
		pin.confirmed &&
		!pin.page?.stale &&
		!pin.action?.pending &&
		!pin.action?.uncertain &&
		!!section;
	const header = (
		<View style={{ gap: 8, paddingBottom: 12 }}>
			<Copy>{section?.name ?? title}</Copy>
			<Copy muted>{pin.activeProfile?.name ?? "Hub"}</Copy>
			<ErrorMessage message={pin.action?.error ?? pin.page?.error ?? null} />
			{!pin.ready ||
			!pin.confirmed ||
			pin.page?.stale ||
			pin.action?.uncertain ? (
				<Action
					disabled={!!pin.action?.pending}
					onPress={() => {
						if (pin.ready) void pin.actions?.reconcile();
						else pin.retry();
					}}
				>
					{pin.ready ? "Refresh section" : "Reconnect"}
				</Action>
			) : null}
			{pin.confirmed && !section ? (
				<Copy>This section no longer exists. Its sessions are kept.</Copy>
			) : null}
			<Action
				tone="quiet"
				onPress={() =>
					navigation.navigate("PinSectionEditor", {
						hubId,
						sectionId,
						title: section?.name ?? title,
					})
				}
			>
				Manage section
			</Action>
		</View>
	);
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{!pin.belongs ? (
				<Copy>
					This hub is no longer selected. Return to Hubs to reconnect.
				</Copy>
			) : pages && !(pin.confirmed && !section) ? (
				<PageList
					header={header}
					pages={pages}
					ready={ready}
					rowKey={sessionKey}
					childRows={(row) => row.children ?? []}
					omitted={(row) =>
						(row.omitted_descendants ?? 0) + (row.more_subagents ?? 0)
					}
					organization={() => null}
					title={(row) => row.title || "Untitled session"}
					detail={(row) =>
						row.ask_pending || row.state === "awaiting"
							? "Needs you"
							: row.state
					}
					empty={
						pin.confirmed && !section
							? "The section was removed; its sessions are kept."
							: "No sessions in this pinned section."
					}
					open={(row) =>
						navigation.navigate("Conversation", {
							hubId,
							ref: row.ref,
							title: row.title,
						})
					}
				/>
			) : (
				<ScrollView contentContainerStyle={styles.padded}>{header}</ScrollView>
			)}
		</SafeAreaView>
	);
}
