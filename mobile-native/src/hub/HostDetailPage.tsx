// A host's detail in the Hub (spec 12): its state in words, its last-known
// version beside the drift tag, its system, live sessions, project roots and
// where it is defined; the hub's last error reaching it while it is offline;
// and Connect for a host the hub isn't attached to or already retrying. Edit
// and Remove come with the guarded host mutations.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef } from "react";
import { hostStatus, systemLabel, VERSION_DRIFT_FOOTER, versionDriftTag } from "../hosts/hostStatus";
import { liveSessionsText } from "../hosts/liveCounts";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row, RowValue } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { HostsNotListed } from "./HostsPage";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen } from "./useHostsOnScreen";

export function HostDetailPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "HostDetail">) {
	const { name } = route.params;
	const { hubName, ready, hosts } = useHubSheet();
	const { state, loadError, liveCount, hubVersion } = useHostsOnScreen();
	const row = state?.rows?.find((candidate) => candidate.name === name);
	// A host removed elsewhere leaves the list: its page goes with it, once.
	const left = useRef(false);
	useEffect(() => {
		if (!state || row || left.current) return;
		left.current = true;
		navigation.goBack();
	}, [state, row, navigation]);
	if (!state || !row || !hosts) return <HostsNotListed hubName={hubName} error={loadError} />;
	const connecting = state.connecting.has(name);
	const status = hostStatus(row, connecting);
	const drift = versionDriftTag(row, hubVersion);
	const system = systemLabel(row);
	const roots = row.roots ?? [];
	// A refusal is moot once the hub has attached the host on its own.
	const connectError = row.attached ? undefined : state.connectErrors.get(name);
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				<Row
					label="Status"
					value={<RowValue text={status.word} tone={status.tone === "attention" ? "attention" : "normal"} />}
					accessibilityLabel={`Status, ${status.word}`}
				/>
				<Row
					label="Version"
					value={<RowValue text={row.hubVersion ?? "Unknown"} tag={drift ? { text: drift, tone: "gray" } : null} />}
					accessibilityLabel={["Version", row.hubVersion ?? "Unknown", drift].filter(Boolean).join(", ")}
				/>
				<Row label="System" value={system ?? "Unknown"} />
				<Row label="Sessions" value={liveSessionsText(liveCount(name), !row.attached)} />
				{roots.length > 0 ? (
					<Row label="Project roots" sub={roots.join("\n")} machineSub />
				) : (
					<Row label="Project roots" value="None" />
				)}
				<Row label="Defined in" value={row.origin === "sidecar" ? "the app or web" : "hub.toml"} />
			</Group>
			{!row.attached && row.lastAttachError ? (
				<>
					<GroupLabel>Last error</GroupLabel>
					<GroupFooter tone="danger" machine>
						{row.lastAttachError}
					</GroupFooter>
				</>
			) : null}
			{status.canConnect || connecting ? (
				<Group>
					<Row
						label={connecting ? "Connecting…" : "Connect"}
						tone="accent"
						disabled={connecting || !ready}
						onPress={() => void hosts.connect(name)}
					/>
				</Group>
			) : null}
			{row.attached && drift ? (
				<GroupFooter>{VERSION_DRIFT_FOOTER}</GroupFooter>
			) : status.footer ? (
				<GroupFooter>{status.footer}</GroupFooter>
			) : null}
			{connectError ? <GroupFooter tone="danger">{connectError}</GroupFooter> : null}
		</GroupedPage>
	);
}
