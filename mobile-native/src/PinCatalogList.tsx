import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { ActivityIndicator, FlatList, View } from "react-native";
import { updating } from "./navigationPages";
import { Copy, ErrorMessage, styles, useColors } from "./ui";
import { Button } from "./sheet/Grouped";

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
	previousChange,
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
	previousChange: boolean;
	error: string | null;
	refresh(): void;
	more(): void;
	open(section: NavigationPinSectionDescriptor): void;
}) {
	const colors = useColors();
	const isUpdating = updating({ loading, error, stale, remaining });
	const showEmpty = connected && loaded && !loading && !pending && !uncertain && !error && sections.length === 0;
	return (
		<FlatList
			data={sections}
			keyExtractor={(section) => section.id}
			contentContainerStyle={{ padding: 20, gap: 12 }}
			ListHeaderComponent={
				<View style={{ gap: 8, paddingBottom: 8 }}>
					<Copy>{hubName}</Copy>
					{loading || pending ? <ActivityIndicator accessibilityLabel="Checking pinned sections" /> : null}
					{!connected ? (
						<Copy muted>You can see pinned sections once the hub is back.</Copy>
					) : uncertain ? (
						<Copy muted>{previousChange ? "Check the previous pin change." : "Check the pinned sections again."}</Copy>
					) : isUpdating ? (
						<Copy muted>Updating…</Copy>
					) : null}
					<ErrorMessage message={error} />
					{connected && (uncertain || error) ? (
						<Button text quiet label="Check again" disabled={loading || pending} onPress={refresh} />
					) : null}
					{showEmpty ? (
						<View style={{ gap: 8 }}>
							<Copy>No pinned sections yet.</Copy>
							<Copy muted>Pin a session from its actions menu to create a section.</Copy>
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
					<Button
						text
						quiet
						label={section.name}
						onPress={() => open(section)}
						accessibilityLabel={`Open ${section.name}`}
					/>
					<Copy muted>
						{section.count} {section.count === 1 ? "session" : "sessions"}
					</Copy>
				</View>
			)}
			ListFooterComponent={
				remaining > 0 ? (
					<Button
						text
						label={`Load more sections (${remaining} remaining)`}
						disabled={!connected || !loaded || loading || uncertain || pending}
						onPress={more}
					/>
				) : null
			}
		/>
	);
}
