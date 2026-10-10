// A host's detail in the Hub (spec 12): its state in words, its last-known
// version beside the drift tag, its system, live sessions and project roots;
// the hub's last error reaching it while it is offline;
// Connect for a host the hub isn't attached to or already retrying; and Edit
// and Remove on every host, hub.toml's included (spec 12), through the guarded
// host mutations.
import { friendlyErrorMessage } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { Alert, Text } from "react-native";
import { whenReady } from "../connectionDisplay";
import { fonts, scaledType, space, uiType } from "../design/tokens";
import { destructiveButton } from "../haptics";
import { hostStatus, systemLabel, VERSION_DRIFT_FOOTER, versionDriftTag } from "../hosts/hostStatus";
import { liveSessionsText } from "../hosts/liveCounts";
import { Group, GroupedPage, GroupFooter, Row, RowValue } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { HostsNotListed } from "../hosts/HostsNotListed";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";
import { useHostsOnScreen, useLeavesWithHost } from "./useHostsOnScreen";

export function HostDetailPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "HostDetail">) {
	const { name } = route.params;
	const { hubId, hubName, ready, canUseConnection, hosts } = useHubSheet();
	const [removing, setRemoving] = useState(false);
	const [removeError, setRemoveError] = useState<string | null>(null);
	const { palette } = useColors();
	const scale = useTextScale();
	const { state, loadError, liveCount, hubVersion } = useHostsOnScreen();
	const row = state?.rows?.find((candidate) => candidate.name === name);
	useLeavesWithHost(state ? !!row : null, navigation.goBack);
	if (!state || !row || !hosts) return <HostsNotListed hubName={hubName} error={loadError} />;
	const connecting = state.connecting.has(name);
	const status = hostStatus(row, connecting);
	const drift = versionDriftTag(row, hubVersion);
	const system = systemLabel(row);
	const roots = row.roots ?? [];
	const connectError = state.connectErrors.get(name);
	const remove = async () => {
		setRemoving(true);
		setRemoveError(null);
		try {
			// The host then leaves the list, and the page goes back with it.
			await hosts.remove(name);
		} catch (refusal) {
			setRemoveError(friendlyErrorMessage(refusal));
		} finally {
			setRemoving(false);
		}
	};
	const confirmRemove = () =>
		Alert.alert(`Remove ${name}?`, "The hub forgets this host. Add it again from the web app.", [
			{ text: "Cancel", style: "cancel" },
			// The alert can outlive the connection it opened on.
			destructiveButton(
				"Remove",
				whenReady(canUseConnection, () => void remove()),
			),
		]);
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
			</Group>
			{!row.attached && row.lastAttachError ? (
				// Raw text the hub reported, at the prototype's footnote size
				// (hub.js:67): too long for a 17pt row label.
				<Group label="Last error">
					<Text
						allowFontScaling={allowFontScaling}
						style={{
							color: palette.dangerInk,
							...scaledType(uiType.footnote, scale),
							fontFamily: fonts.mono,
							paddingHorizontal: space.rowInset,
							paddingVertical: space.rowPadding,
						}}
					>
						{row.lastAttachError}
					</Text>
				</Group>
			) : null}
			<Group>
				{!row.attached && (status.canConnect || connecting) ? (
					<Row
						label={connecting ? "Connecting…" : "Connect"}
						tone="accent"
						disabled={connecting || !ready}
						onPress={() => void hosts.connect(name)}
					/>
				) : null}
				<Row
					label="Edit"
					tone="accent"
					chevron
					disabled={!ready || removing}
					onPress={() => navigation.navigate("HostEdit", { hubId, name })}
				/>
				<Row
					label={removing ? "Removing…" : "Remove"}
					accessibilityLabel="Remove"
					tone="danger"
					disabled={!ready || removing}
					onPress={confirmRemove}
				/>
			</Group>
			{row.attached && drift ? (
				<GroupFooter>{VERSION_DRIFT_FOOTER}</GroupFooter>
			) : status.footer ? (
				<GroupFooter>{status.footer}</GroupFooter>
			) : null}
			{connectError ? <GroupFooter tone="danger">{connectError}</GroupFooter> : null}
			{removeError ? <GroupFooter tone="danger">{removeError}</GroupFooter> : null}
		</GroupedPage>
	);
}
