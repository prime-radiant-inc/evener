// New session's per-launch options, until PR 10 gives Access and More options
// their own rows: today's session options, shown open on a page of their own.
import { View } from "react-native";
import { useStore } from "zustand";
import { LaunchOverrides } from "../LaunchOverrides";
import { GroupedPage } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { useNewSession } from "./newSessionContext";

export function SessionOptionsPage() {
	const { store, hubId, client, ready } = useNewSession();
	const form = useStore(store);
	const cwd = form.cwd.trim();
	return (
		<GroupedPage>
			<SheetStatus />
			<View style={{ padding: 16 }}>
				<LaunchOverrides
					key={JSON.stringify([hubId, cwd])}
					client={ready ? client : null}
					cwd={cwd}
					value={form.launchOverrides}
					onChange={form.setLaunchOverrides}
					disabled={!ready || form.submitting || !cwd}
					defaultOpen
				/>
			</View>
		</GroupedPage>
	);
}
