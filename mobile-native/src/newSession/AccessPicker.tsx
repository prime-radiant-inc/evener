// New session's Access (spec 11; ruling 14): the four access levels, checked
// on the one the session gets, and Network inside a sandbox. Access is the
// launch override `sandbox`; choosing the level the hub already defaults to
// for this project drops the override, so the setup keeps following the hub.
import { useStore } from "zustand";
import { Group, GroupedPage, GroupFooter, GroupGap, Row, SwitchRow } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { ACCESS_LEVELS, accessOf, networkApplies } from "./launchSetup";
import { useNewSession } from "./newSessionContext";

/** The schema's sandbox when the hub sets none: no sandbox. */
const NO_SANDBOX = "off";

export function AccessPicker() {
	const { store, launchDefaults: defaults } = useNewSession();
	const { launchOverrides, submitting, setLaunchOverrides } = useStore(store);
	const access = accessOf(launchOverrides.sandbox, defaults?.sandbox);
	// Unknown until the hub answers; until then every choice is the session's own.
	const hubMode = defaults ? defaults.sandbox || NO_SANDBOX : null;
	const choose = (mode: string) => {
		const next = { ...launchOverrides };
		if (mode === hubMode) delete next.sandbox;
		else next.sandbox = mode;
		setLaunchOverrides(next);
	};
	return (
		<GroupedPage>
			<SheetStatus />
			<GroupGap />
			<Group>
				{ACCESS_LEVELS.map((level) => (
					<Row
						key={level.mode}
						label={level.label}
						sub={level.detail}
						checked={level.mode === access.mode}
						disabled={submitting}
						onPress={() => choose(level.mode)}
					/>
				))}
			</Group>
			{networkApplies(access) ? (
				<>
					<GroupGap />
					<Group>
						<SwitchRow
							label="Network"
							sub="Lets the session's commands reach the internet"
							value={launchOverrides.sandboxNet ?? defaults?.sandboxNet ?? true}
							disabled={submitting}
							onChange={(on) => setLaunchOverrides({ ...launchOverrides, sandboxNet: on })}
						/>
					</Group>
				</>
			) : null}
			<GroupFooter>Access is fixed once the session starts.</GroupFooter>
		</GroupedPage>
	);
}
