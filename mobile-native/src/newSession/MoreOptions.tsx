// New session's More options (spec 11; ruling 14): the few launch settings
// worth touching on a phone, each a segmented control whose first segment,
// Default, leaves the setting to the hub, with the hub's default for this
// host and project under it. A value the control doesn't offer (from a saved
// draft or a remembered start) lights no segment, and the footer names it.
import type { LaunchConfigLayer } from "@evener/appwire-client";
import { View } from "react-native";
import { useStore } from "zustand";
import { GroupedPage, GroupFooter, GroupLabel, Segmented } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { useNewSession } from "./newSessionContext";
import { type LaunchDefaults, useLaunchDefaults } from "./useLaunchDefaults";

type Field = "contextStrategy" | "maxSubagentDepth" | "maxRounds";

interface Setting {
	field: Field;
	label: string;
	/** The values offered after Default, as the launch schema spells them. */
	values: readonly (string | number)[];
}

const SETTINGS: readonly Setting[] = [
	{ field: "contextStrategy", label: "Context strategy", values: ["compact", "session-log", "ooda"] },
	{ field: "maxSubagentDepth", label: "Max subagent depth", values: [1, 2, 3, 5] },
	// Spec 11's word; the schema calls it "Max rounds". -1 is no limit.
	{ field: "maxRounds", label: "Max turns", values: [100, 500, -1] },
];

/** The segment that leaves the setting to the hub. */
const DEFAULT = "default";

/** How a value reads, on a segment and in a sentence. */
function spoken(value: string | number, sentence = false): string {
	if (value === -1) return sentence ? "no limit" : "No limit";
	return String(value);
}

function footer(value: string | number | undefined, hubDefault: string | number | undefined, offered: boolean) {
	const uses = value !== undefined && !offered ? `This session uses ${spoken(value, true)}.` : null;
	const hub =
		hubDefault !== undefined
			? `The hub's default is ${spoken(hubDefault, true)}.`
			: uses
				? null
				: "Default uses the hub's default.";
	return [uses, hub].filter(Boolean).join(" ");
}

export function MoreOptions() {
	const { store } = useNewSession();
	const { source, cwd, launchOverrides, submitting, setLaunchOverrides } = useStore(store);
	const defaults: LaunchDefaults | null = useLaunchDefaults(source, cwd);
	const set = (field: Field, value: string | number | null) => {
		const next: LaunchConfigLayer = { ...launchOverrides };
		if (value === null) delete next[field];
		else Object.assign(next, { [field]: value });
		setLaunchOverrides(next);
	};
	return (
		<GroupedPage>
			<SheetStatus />
			{SETTINGS.map((setting) => {
				const value = launchOverrides[setting.field];
				const offered = value === undefined || setting.values.includes(value);
				return (
					<View key={setting.field}>
						<GroupLabel>{setting.label}</GroupLabel>
						<Segmented
							label={setting.label}
							options={[
								{ value: DEFAULT, label: "Default" },
								...setting.values.map((option) => ({ value: String(option), label: spoken(option) })),
							]}
							value={value === undefined ? DEFAULT : offered ? String(value) : null}
							onChange={(key) =>
								set(
									setting.field,
									key === DEFAULT ? null : (setting.values.find((option) => String(option) === key) ?? null),
								)
							}
							disabled={submitting}
						/>
						<GroupFooter>{footer(value, defaults?.[setting.field], offered)}</GroupFooter>
					</View>
				);
			})}
			<GroupFooter>
				Everything else uses the hub's launch defaults. Edit them from the Hub, under Launch defaults.
			</GroupFooter>
		</GroupedPage>
	);
}
