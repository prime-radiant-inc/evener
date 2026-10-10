// New session's host picker (spec 11; rulings 3 and 4): the hub's own machine,
// named after the hub, then each of the hub's hosts. A connected host shows its
// system and live sessions; an offline one shows its state, can't be chosen,
// and offers Connect when the hub isn't already reaching for it, saying why
// when the hub refused. Choosing a host moves the form there (ruling 17) and
// goes back to it.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { Pressable, Text, View } from "react-native";
import { useStore } from "zustand";
import { LOCAL_HOST } from "@evener/appwire-client";
import { HostsNotListed } from "../hosts/HostsNotListed";
import { hostStatus, systemLabel } from "../hosts/hostStatus";
import { liveSessionsText } from "../hosts/liveCounts";
import { useFleetOnScreen } from "../hosts/useFleetOnScreen";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";

export function HostPicker({ navigation }: NativeStackScreenProps<NewSessionRoutes, "Host">) {
	const { store, hubName, ready, hosts, live, hostLabel } = useNewSession();
	const source = useStore(store, (form) => form.source);
	const { state, loadError, liveCount } = useFleetOnScreen(hosts, live, ready);
	const choose = (host: string) => {
		void store.getState().changeHost(host, hostLabel(host));
		navigation.goBack();
	};
	if (!state?.rows) return <HostsNotListed hubName={hubName} error={loadError} />;
	const rows = state.rows;
	return (
		<GroupedPage>
			<SheetStatus />
			<Group>
				<Row
					label={hostLabel(LOCAL_HOST)}
					sub={liveSessionsText(liveCount(LOCAL_HOST), false)}
					checked={source === LOCAL_HOST}
					onPress={() => choose(LOCAL_HOST)}
				/>
				{rows.map((row) => {
					const connecting = state.connecting.has(row.name);
					const status = hostStatus(row, connecting);
					if (row.attached)
						return (
							<Row
								key={row.name}
								label={row.name}
								sub={[systemLabel(row), liveSessionsText(liveCount(row.name), false)].filter(Boolean).join(" · ")}
								checked={source === row.name}
								onPress={() => choose(row.name)}
							/>
						);
					return (
						<View key={row.name} style={{ flexDirection: "row", alignItems: "center" }}>
							<View style={{ flex: 1 }}>
								<Row label={row.name} sub={status.word} checked={source === row.name} disabled />
							</View>
							{status.canConnect || connecting ? (
								<ConnectButton
									host={row.name}
									connecting={connecting}
									disabled={connecting || !ready}
									onPress={() => void hosts?.connect(row.name)}
								/>
							) : null}
						</View>
					);
				})}
			</Group>
			{rows.map((row) => {
				const refusal = state.connectErrors.get(row.name);
				return refusal ? <GroupFooter key={row.name} tone="danger">{`${row.name}: ${refusal}`}</GroupFooter> : null;
			})}
		</GroupedPage>
	);
}

/** An offline host's Connect: accent, a 44pt target of its own beside the row,
 * so VoiceOver reaches it apart from the row it can't choose. */
function ConnectButton({
	host,
	connecting,
	disabled,
	onPress,
}: {
	host: string;
	connecting: boolean;
	disabled: boolean;
	onPress(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Connect ${host}`}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={onPress}
			style={{ minHeight: 44, minWidth: 44, justifyContent: "center", paddingHorizontal: 16 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: connecting ? palette.inkMid : palette.accentInk, fontSize: 17 * scale }}
			>
				{connecting ? "Connecting…" : "Connect"}
			</Text>
		</Pressable>
	);
}
