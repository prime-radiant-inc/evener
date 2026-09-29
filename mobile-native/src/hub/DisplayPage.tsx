// The Hub's Display page (spec 12): this phone's appearance and reading font,
// and the hub's default detail level, which Default detail level pushes to
// choose. "Show model on Board rows" waits for S17 (ruling 8), so it isn't
// here.
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
import { GroupedPage, GroupFooter, GroupLabel, Group, Row, Segmented } from "../sheet/Grouped";
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
			{transcript?.support === "unsupported" ? (
				<>
					<GroupLabel>Default detail level</GroupLabel>
					<GroupFooter>This hub doesn't keep a default detail level.</GroupFooter>
				</>
			) : (
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
		</GroupedPage>
	);
}
