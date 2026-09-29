// The Hub's Hosts page (spec 12; rulings 3, 4 and 5): the hub's own machine,
// named after the hub, then each host with its state in words, its system,
// its live sessions, and its version with a gray tag when it differs from the
// hub's. The page reads the hub's hosts every 2 seconds while it is on screen,
// keeps its last rows through a failed read, and reads afresh on a new connection.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect } from "react";
import { useConnectionStatusText } from "../board/connectionStatus";
import { HostsNotListed } from "../hosts/HostsNotListed";
import { hostStatus, systemLabel, versionDriftTag } from "../hosts/hostStatus";
import { LOCAL_HOST, liveSessionsText } from "../hosts/liveCounts";
import { Group, GroupedPage, GroupFooter, Row, RowValue } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { hubConnectionWord } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen } from "./useHostsOnScreen";

export function HostsPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "Hosts">) {
	const { hubId, hubName, ready } = useHubSheet();
	const connectionLine = useConnectionStatusText();
	const { state, loadError, liveCount, hubVersion } = useHostsOnScreen();
	// A notice's Details opens the host it named, once (ruling 25).
	const focus = route.params.focus;
	useEffect(() => {
		if (!focus) return;
		navigation.navigate("HostDetail", { hubId, name: focus });
		navigation.setParams({ focus: undefined });
	}, [focus, hubId, navigation]);
	if (!state?.rows) return <HostsNotListed hubName={hubName} error={loadError} />;
	// The hub's own machine is as reachable as the hub, in the Hubs page's words.
	const word = hubConnectionWord(ready, connectionLine);
	const ownLine = ready ? `${word} · ${liveSessionsText(liveCount(LOCAL_HOST), false)}` : word;
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				<Row
					icon="server.rack"
					label={hubName}
					sub={ownLine}
					value={hubVersion}
					chevron
					onPress={() => navigation.navigate("OwnHost", { hubId })}
				/>
				{state.rows.map((row) => {
					const status = hostStatus(row, state.connecting.has(row.name));
					const sub = [status.word, systemLabel(row), liveSessionsText(liveCount(row.name), !row.attached)]
						.filter(Boolean)
						.join(" · ");
					const drift = versionDriftTag(row, hubVersion);
					return (
						<Row
							key={row.name}
							icon="server.rack"
							label={row.name}
							sub={sub}
							value={<RowValue text={row.hubVersion} tag={drift ? { text: drift, tone: "gray" } : null} />}
							accessibilityLabel={[row.name, sub, row.hubVersion, drift].filter(Boolean).join(", ")}
							chevron
							onPress={() => navigation.navigate("HostDetail", { hubId, name: row.name })}
						/>
					);
				})}
			</Group>
			<GroupFooter>
				Hosts come from hub.toml or were added in the web app. Add hosts from the web app; they need an SSH address and
				a key.
			</GroupFooter>
		</GroupedPage>
	);
}
