import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { FlatList, View } from "react-native";
import { updating } from "./navigationPages";
import { Copy, ErrorMessage, styles, useColors } from "./ui";
import { Button } from "./sheet/Grouped";
import { Spinner } from "./sheet/Spinner";

/** What the catalog says about the list it shows, as its `pin-status:<reason>`
 * line. */
const CATALOG_STATUS = {
	offline: "You can see pinned categories once the hub is back.",
	"previous-change": "Check the previous pin change.",
	unread: "Check the pinned categories again.",
	updating: "Updating…",
};

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
	const status: keyof typeof CATALOG_STATUS | null = !connected
		? "offline"
		: uncertain
			? previousChange
				? "previous-change"
				: "unread"
			: isUpdating
				? "updating"
				: null;
	const showEmpty = connected && loaded && !loading && !pending && !uncertain && !error && sections.length === 0;
	return (
		<FlatList
			data={sections}
			keyExtractor={(section) => section.id}
			contentContainerStyle={{ padding: 20, gap: 12 }}
			ListHeaderComponent={
				<View style={{ gap: 8, paddingBottom: 8 }}>
					<Copy>{hubName}</Copy>
					{loading || pending ? <Spinner label="Checking pinned categories" /> : null}
					{status ? (
						<View testID={`pin-status:${status}`}>
							<Copy muted>{CATALOG_STATUS[status]}</Copy>
						</View>
					) : null}
					<ErrorMessage message={error} />
					{connected && (uncertain || error) ? (
						<Button text quiet label="Check again" disabled={loading || pending} onPress={refresh} />
					) : null}
					{showEmpty ? (
						<View style={{ gap: 8 }}>
							<Copy>No pinned categories yet.</Copy>
							<Copy muted>Pin a session from its actions menu to create a category.</Copy>
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
						label={`Load more categories (${remaining} remaining)`}
						disabled={!connected || !loaded || loading || uncertain || pending}
						onPress={more}
					/>
				) : null
			}
		/>
	);
}
