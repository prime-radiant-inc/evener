import type { ReactNode } from "react";
import { ActivityIndicator, ScrollView, TextInput, View } from "react-native";
import { updating } from "./navigationPages";
import type { PinAssignmentSelection } from "./pinAssignmentDrafts";
import { Choice, Copy, ErrorMessage, styles, useColors } from "./ui";
import { Button } from "./sheet/Grouped";

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
	previousChange: boolean;
	error: string | null;
	refresh(): void;
	more(): void;
	save(): void;
	unpin(): void;
	close(): void;
}

/** Why the editor can't change the assignment right now, as its
 * `pin-status:<reason>` line says it. */
const BLOCKED = {
	"previous-change": "Check the previous pin change before editing it.",
	unread: "Check this session's pins before editing.",
	checking: "Checking the pin assignment…",
	offline: "You can change pin assignments once the hub is back.",
};

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
	previousChange,
	error,
	refresh,
	more,
	save,
	unpin,
	close,
}: PinAssignmentEditorProps) {
	const colors = useColors();
	const blocked = pending || uncertain || !canEdit || !connected;
	const valid =
		(selection?.kind === "existing" && sections.some((section) => section.id === selection.sectionId)) ||
		(selection?.kind === "new" && validName(selection.name));
	const reason: keyof typeof BLOCKED | null = uncertain
		? previousChange
			? "previous-change"
			: "unread"
		: pending
			? "checking"
			: !connected
				? "offline"
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
				{reason ? (
					<View testID={`pin-status:${reason}`}>
						<Copy muted>{BLOCKED[reason]}</Copy>
					</View>
				) : null}
				<View accessibilityRole="radiogroup" accessibilityLabel="Pinned section" style={{ gap: 4 }}>
					{sections.map((section) => (
						<Choice
							key={section.id}
							label={`${section.name} · ${section.count} ${section.count === 1 ? "session" : "sessions"}`}
							selected={selection?.kind === "existing" && selection.sectionId === section.id}
							disabled={blocked}
							onPress={() => change({ kind: "existing", sectionId: section.id })}
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
					{selection?.kind === "new" && !validName(selection.name) ? <Copy muted>Use 1–80 characters.</Copy> : null}
				</View>
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<Button
						primary
						compact
						label={selection?.kind === "new" ? "Create and pin" : "Pin to section"}
						disabled={blocked || !valid}
						onPress={save}
					/>
					<Button text quiet label="Unpin" disabled={blocked} onPress={unpin} />
				</View>
				{loading ? <ActivityIndicator accessibilityLabel="Loading pinned sections" /> : null}
				{updating({ loading, error, stale, remaining }) ? <Copy muted>Updating…</Copy> : null}
				{!loading && remaining > 0 ? (
					<Button text label={`Load more sections (${remaining} remaining)`} disabled={blocked} onPress={more} />
				) : null}
				{uncertain ? (
					<Button text quiet label="Check again" disabled={!connected || loading || pending} onPress={refresh} />
				) : null}
				<Button text quiet label="Close" onPress={close} />
			</ScrollView>
		</View>
	);
}
