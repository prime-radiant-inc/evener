// The hub's own machine in the Hub's Hosts (audit M1): its state, the hub's
// version and its live sessions. The hub's hosts list carries only the hosts
// it reaches over SSH, so this machine has no system, roots or origin to show,
// and nothing to connect, edit or remove.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useConnectionStatusText } from "../board/connectionStatus";
import { LOCAL_HOST, liveSessionsText } from "../hosts/liveCounts";
import { Group, GroupedPage, Row } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { hubConnectionWord } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen } from "./useHostsOnScreen";

export function OwnHostPage(_props: NativeStackScreenProps<HubRoutes, "OwnHost">) {
	const { ready } = useHubSheet();
	const connectionLine = useConnectionStatusText();
	const { liveCount, hubVersion } = useHostsOnScreen();
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				<Row label="Status" value={hubConnectionWord(ready, connectionLine)} />
				<Row label="Version" value={hubVersion ?? "Unknown"} />
				<Row label="Sessions" value={liveSessionsText(liveCount(LOCAL_HOST), false)} />
			</Group>
		</GroupedPage>
	);
}
