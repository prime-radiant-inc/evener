import type { ReactNode } from "react";
import { ActivityIndicator, ScrollView, TextInput, View } from "react-native";
import type { PinAssignmentSelection } from "./pinAssignmentDrafts";
import { Action, Choice, Copy, ErrorMessage, styles, useColors } from "./ui";

export type PinAssignmentSection = { id: string; name: string; count: number };
export interface PinAssignmentEditorProps {
	status?: ReactNode;
	title: string;
	hubName: string;
	sections: readonly PinAssignmentSection[];
	selection: PinAssignmentSelection;
	change(selection: PinAssignmentSelection): void;
	connected: boolean;
	canEdit: boolean;
	loading: boolean;
	stale: boolean;
	remaining: number;
	pending: boolean;
	uncertain: boolean;
	error: string | null;
	refresh(): void;
	more(): void;
	save(): void;
	unpin(): void;
	close(): void;
}
function validName(name: string) {
	const length = Array.from(name.trim()).length;
	return length >= 1 && length <= 80;
}
export function PinAssignmentEditor({
	status,
	title,
	hubName,
	sections,
	selection,
	change,
	connected,
	canEdit,
	loading,
	stale,
	remaining,
	pending,
	uncertain,
	error,
	refresh,
	more,
	save,
	unpin,
	close,
}: PinAssignmentEditorProps) {
	const colors = useColors();
	const blocked =
		pending || uncertain || !canEdit || !connected || stale || loading;
	const valid =
		(selection?.kind === "existing" &&
			sections.some((section) => section.id === selection.sectionId)) ||
		(selection?.kind === "new" && validName(selection.name));
	const reason = uncertain
		? "Refresh to confirm the previous pin change before editing it."
		: pending
			? "Checking the pin assignment…"
			: !connected
				? "Reconnect to change pin assignments."
				: stale
					? "Refresh the sections before changing a pin assignment."
					: null;
	return (
		<View style={[styles.fill, { backgroundColor: colors.background }]}>
			<ScrollView
				keyboardShouldPersistTaps="handled"
				keyboardDismissMode="on-drag"
				contentContainerStyle={{ padding: 20, gap: 16 }}
			>
				{status}
				<View
					style={[
						{
							gap: 8,
							paddingVertical: 12,
							borderBottomWidth: 1,
							borderColor: colors.border,
						},
					]}
				>
					<Copy>{title}</Copy>
					<Copy muted>{hubName}</Copy>
				</View>
				<ErrorMessage message={error} />
				{reason ? <Copy muted>{reason}</Copy> : null}
				<View
					accessibilityRole="radiogroup"
					accessibilityLabel="Pinned section"
					style={{ gap: 4 }}
				>
					{sections.map((section) => (
						<Choice
							key={section.id}
							label={`${section.name} · ${section.count} ${section.count === 1 ? "session" : "sessions"}`}
							selected={
								selection?.kind === "existing" &&
								selection.sectionId === section.id
							}
							disabled={blocked}
							onPress={() =>
								change({ kind: "existing", sectionId: section.id })
							}
						/>
					))}
				</View>
				<View style={{ gap: 8 }}>
					<Choice
						label="Create a new pinned section"
						selected={selection?.kind === "new"}
						disabled={blocked}
						onPress={() =>
							change({
								kind: "new",
								name: selection?.kind === "new" ? selection.name : "",
							})
						}
					/>
					<TextInput
						accessibilityLabel="New pinned section name"
						value={selection?.kind === "new" ? selection.name : ""}
						placeholder="Section name"
						editable={!blocked && selection?.kind === "new"}
						onChangeText={(name) => change({ kind: "new", name })}
						style={[
							styles.input,
							{
								color: colors.text,
								borderColor: colors.border,
								backgroundColor: colors.surface,
							},
						]}
					/>
					{selection?.kind === "new" && !validName(selection.name) ? (
						<Copy muted>Use 1–80 characters.</Copy>
					) : null}
				</View>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<Action tone="primary" disabled={blocked || !valid} onPress={save}>
						{selection?.kind === "new" ? "Create and pin" : "Pin to section"}
					</Action>
					<Action tone="quiet" disabled={blocked} onPress={unpin}>
						Unpin
					</Action>
				</View>
				{loading ? (
					<ActivityIndicator accessibilityLabel="Loading pinned sections" />
				) : null}
				{!loading && remaining > 0 ? (
					<Action disabled={blocked} onPress={more}>
						{`Load more sections (${remaining} remaining)`}
					</Action>
				) : null}
				{stale || uncertain ? (
					<Action
						tone="quiet"
						disabled={!connected || loading || pending}
						onPress={refresh}
					>
						Refresh sections
					</Action>
				) : null}
				<Action tone="quiet" onPress={close}>
					Close
				</Action>
			</ScrollView>
		</View>
	);
}
