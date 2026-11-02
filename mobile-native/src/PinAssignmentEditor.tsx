import { type ReactNode, useMemo } from "react";
import { ScrollView, View } from "react-native";
import { updating } from "./navigationPages";
import type { PinAssignmentSelection } from "./pinAssignmentDrafts";
import { Copy, ErrorMessage, styles, useColors } from "./ui";
import { Button, Group, Row, TextFieldRow } from "./sheet/Grouped";
import { Spinner } from "./sheet/Spinner";
import { CATEGORY_NAME_MAX_CODE_POINTS } from "./navigationActionRepository";

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
	return length >= 1 && length <= CATEGORY_NAME_MAX_CODE_POINTS;
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
	const chosenId = selection?.kind === "existing" ? selection.sectionId : null;
	const creating = selection?.kind === "new";
	const rows = useMemo(
		() =>
			sections.map((section) => ({
				section,
				label: `${section.name} · ${section.count} ${section.count === 1 ? "session" : "sessions"}`,
				checked: chosenId === section.id,
			})),
		[sections, chosenId],
	);
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
				{rows.length > 0 ? (
					<Group label="Pinned categories" section="pin-categories">
						{rows.map(({ section, label, checked }) => (
							<Row
								key={section.id}
								label={label}
								checked={checked}
								disabled={blocked}
								onPress={() => change({ kind: "existing", sectionId: section.id })}
							/>
						))}
					</Group>
				) : null}
				<Group label="New category" section="pin-new-category">
					<Row
						label="Create a new category"
						checked={creating}
						disabled={blocked}
						onPress={() =>
							change({
								kind: "new",
								name: selection?.kind === "new" ? selection.name : "",
							})
						}
					/>
					<TextFieldRow
						label="New category name"
						value={creating ? (selection?.name ?? "") : ""}
						onChangeText={(name) => change({ kind: "new", name })}
						disabled={blocked || !creating}
						machine={false}
						placeholder="Category name"
					/>
				</Group>
				{creating && !validName(selection?.kind === "new" ? selection.name : "") ? (
					<Copy muted>Use 1–{CATEGORY_NAME_MAX_CODE_POINTS} characters.</Copy>
				) : null}
				<View style={[styles.row, { flexWrap: "wrap" }]}>
					<Button
						primary
						compact
						label={creating ? "Create and pin" : "Pin to category"}
						disabled={blocked || !valid}
						onPress={save}
					/>
					<Button text quiet label="Unpin" disabled={blocked} onPress={unpin} />
				</View>
				{loading ? <Spinner label="Loading pinned categories" /> : null}
				{updating({ loading, error, stale, remaining }) ? <Copy muted>Updating…</Copy> : null}
				{!loading && remaining > 0 ? (
					<Button text label={`Load more categories (${remaining} remaining)`} disabled={blocked} onPress={more} />
				) : null}
				{uncertain ? (
					<Button text quiet label="Check again" disabled={!connected || loading || pending} onPress={refresh} />
				) : null}
				<Button text quiet label="Close" onPress={close} />
			</ScrollView>
		</View>
	);
}
