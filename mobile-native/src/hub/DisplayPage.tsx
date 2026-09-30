// The Hub's Display page (spec 12): this phone's appearance and reading font,
// the hub's default detail level, which Default detail level pushes to
// choose, and whether Board rows show their model (S17's model name on
// navigation summaries, ruling 8).
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { useDisplayChoices, useDisplayPreferences } from "../display/displayContext";
import {
	APPEARANCE_CHOICES,
	APPEARANCE_LABELS,
	type AppearanceChoice,
	type DisplayChoices,
	type ReadingFont,
} from "../display/displayPreferences";
import { useNativePreferences } from "../NativePreferencesProvider";
import { detailLevel } from "../session/detailLevels";
import { GroupedPage, GroupFooter, GroupLabel, Group, Row, Segmented, SwitchRow } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import type { HubRoutes } from "./hubSheetContext";

const APPEARANCES: readonly { value: AppearanceChoice; label: string }[] = APPEARANCE_CHOICES.map((value) => ({
	value,
	label: APPEARANCE_LABELS[value],
}));
const READING_FONTS: readonly { value: ReadingFont; label: string }[] = [
	{ value: "serif", label: "Serif" },
	{ value: "sans", label: "Sans" },
];

const NOT_SAVED = "This choice applies now but couldn't be saved on this phone.";

export function DisplayPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "Display">) {
	const prefs = useDisplayPreferences();
	const choices = useDisplayChoices();
	// Which group's last choice failed to store: its footer says so.
	const [unsaved, setUnsaved] = useState<keyof DisplayChoices | null>(null);
	const choose = (change: Partial<DisplayChoices>, field: keyof DisplayChoices) => {
		try {
			prefs?.set(change);
			setUnsaved(null);
		} catch {
			setUnsaved(field);
		}
	};
	const { snapshot } = useNativePreferences();
	const transcript = snapshot?.transcriptMobile;
	// The hub's saved level, not a change still waiting to save.
	const config = transcript?.confirmed?.config;
	const levelLabel = config
		? config.content.kind === "preset"
			? detailLevel(config.content.level).label
			: "Custom"
		: undefined;
	return (
		<GroupedPage>
			<SheetStatus />
			<GroupLabel>Appearance</GroupLabel>
			<Segmented
				label="Appearance"
				options={APPEARANCES}
				value={choices.appearance}
				onChange={(appearance) => choose({ appearance }, "appearance")}
			/>
			{unsaved === "appearance" ? <GroupFooter tone="danger">{NOT_SAVED}</GroupFooter> : null}
			<GroupLabel>Reading font</GroupLabel>
			<Segmented
				label="Reading font"
				options={READING_FONTS}
				value={choices.readingFont}
				onChange={(readingFont) => choose({ readingFont }, "readingFont")}
			/>
			<GroupFooter>For what agents write: messages, plans and documents.</GroupFooter>
			{unsaved === "readingFont" ? <GroupFooter tone="danger">{NOT_SAVED}</GroupFooter> : null}
			{/* A hub that keeps no default level shows nothing here (spec 14). */}
			{transcript?.support === "unsupported" ? null : (
				<>
					{/* The row names the setting; a label above would say it twice. */}
					<Group>
						<Row
							label="Default detail level"
							value={levelLabel}
							chevron
							onPress={() => navigation.navigate("DetailLevel", { hubId: route.params.hubId })}
						/>
					</Group>
					<GroupFooter>Each session can override this from its menu.</GroupFooter>
				</>
			)}
			<Group>
				<SwitchRow
					label="Show model on Board rows"
					value={choices.showModel}
					onChange={(showModel) => choose({ showModel }, "showModel")}
				/>
			</Group>
			{unsaved === "showModel" ? <GroupFooter tone="danger">{NOT_SAVED}</GroupFooter> : null}
		</GroupedPage>
	);
}
