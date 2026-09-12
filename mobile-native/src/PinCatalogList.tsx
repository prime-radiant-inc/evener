import { ActivityIndicator, FlatList, View } from "react-native";
import type { NavigationPinSectionDescriptor } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function PinCatalogList({
	sections,
	hubName,
	connected,
	loaded,
	loading,
	stale,
	remaining,
	pending,
	uncertain,
	error,
	refresh,
	more,
	open,
}: {
	sections: readonly NavigationPinSectionDescriptor[];
	hubName: string;
	connected: boolean;
	loaded: boolean;
	loading: boolean;
	stale: boolean;
	remaining: number;
	pending: boolean;
	uncertain: boolean;
	error: string | null;
	refresh(): void;
	more(): void;
	open(section: NavigationPinSectionDescriptor): void;
}) {
	const colors = useColors();
	const showEmpty =
		connected &&
		loaded &&
		!loading &&
		!pending &&
		!uncertain &&
		!error &&
		!stale &&
		sections.length === 0;
	return (
		<FlatList
			data={sections}
			keyExtractor={(section) => section.id}
			contentContainerStyle={{ padding: 20, gap: 12 }}
			ListHeaderComponent={
				<View style={{ gap: 8, paddingBottom: 8 }}>
					<Copy>{hubName}</Copy>
					{loading || pending ? (
						<ActivityIndicator accessibilityLabel="Checking pinned sections" />
					) : null}
					{!connected ? (
						<Copy muted>Reconnect to view pinned sections.</Copy>
					) : stale ? (
						<Copy muted>Refresh to see the latest pinned sections.</Copy>
					) : uncertain ? (
						<Copy muted>Refresh to confirm the previous pin change.</Copy>
					) : null}
					<ErrorMessage message={error} />
					<Action disabled={loading || pending} onPress={refresh} tone="quiet">
						{connected ? "Refresh sections" : "Reconnect"}
					</Action>
					{showEmpty ? (
						<View style={{ gap: 8 }}>
							<Copy>No pinned sections yet.</Copy>
							<Copy muted>
								Pin a session from its actions menu to create a section.
							</Copy>
						</View>
					) : null}
				</View>
			}
			renderItem={({ item: section }) => (
				<View
					style={[
						styles.card,
						{
							gap: 4,
							borderColor: colors.border,
							backgroundColor: colors.surface,
						},
					]}
				>
					<Action
						tone="quiet"
						onPress={() => open(section)}
						label={`Open ${section.name}`}
					>
						{section.name}
					</Action>
					<Copy muted>
						{section.count} {section.count === 1 ? "session" : "sessions"}
					</Copy>
				</View>
			)}
			ListFooterComponent={
				remaining > 0 ? (
					<Action
						disabled={
							!connected || !loaded || loading || stale || uncertain || pending
						}
						onPress={more}
					>
						{`Load more sections (${remaining} remaining)`}
					</Action>
				) : null
			}
		/>
	);
}
