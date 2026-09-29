// New session's Access (spec 11; ruling 14): the four access levels, checked
// on the one the session gets once that is known, and Network inside a
// sandbox. Access is the launch override `sandbox` and Network is
// `sandboxNet`; choosing what the hub already defaults to for this project
// drops the override, so the setup keeps following the hub.
import { useStore } from "zustand";
import { Group, GroupedPage, GroupFooter, Row, SwitchRow } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { ACCESS_LEVELS, knownAccess, networkApplies } from "./launchSetup";
import { useNewSession } from "./newSessionContext";

/** The schema's sandbox when the hub sets none: no sandbox. */
const NO_SANDBOX = "off";

export function AccessPicker() {
	const { store, launchDefaults: defaults } = useNewSession();
	const { launchOverrides, submitting, setLaunchOverrides } = useStore(store);
	const access = knownAccess(launchOverrides.sandbox, defaults);
	// Unknown until the hub answers; until then every choice is the session's own.
	const hubMode = defaults ? defaults.sandbox || NO_SANDBOX : null;
	const choose = (mode: string) => {
		const next = { ...launchOverrides };
		if (mode === hubMode) delete next.sandbox;
		else next.sandbox = mode;
		// Network has no effect outside a sandbox, so it doesn't outlive one.
		if (mode === NO_SANDBOX) delete next.sandboxNet;
		setLaunchOverrides(next);
	};
	// The schema's sandbox_net is on unless the hub says otherwise.
	const hubNetwork = defaults ? (defaults.sandboxNet ?? true) : null;
	const setNetwork = (on: boolean) => {
		const next = { ...launchOverrides };
		if (on === hubNetwork) delete next.sandboxNet;
		else next.sandboxNet = on;
		setLaunchOverrides(next);
	};
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				{ACCESS_LEVELS.map((level) => (
					<Row
						key={level.mode}
						label={level.label}
						sub={level.detail}
						checked={level.mode === access?.mode}
						disabled={submitting}
						onPress={() => choose(level.mode)}
					/>
				))}
			</Group>
			{access && networkApplies(access) ? (
				<>
					<Group>
						<SwitchRow
							label="Network"
							sub="Lets the session's commands reach the internet"
							value={launchOverrides.sandboxNet ?? hubNetwork ?? true}
							disabled={submitting}
							onChange={setNetwork}
						/>
					</Group>
				</>
			) : null}
			<GroupFooter>Access is fixed once the session starts.</GroupFooter>
		</GroupedPage>
	);
}
