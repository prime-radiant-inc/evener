// What a subagent's own session adds to the Session (spec 9, rulings 10 and
// 30): its row in the coordinator's shared tree, read again while this screen
// is in front; the stop requests you sent, settled against that tree with a
// toast; and, while the hub takes no message for the subagent, the bar where
// its composer would be. A stop goes to the hub directly when the
// coordinator's hub can make one (S6), and is asked of the coordinator
// otherwise.
import { type ThreadCapabilities, WireError } from "@evener/appwire-client";
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { Alert } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { isMethodNotFound } from "../wireErrors";
import { returnToSession, type SessionNavigation } from "../session/returnToSession";
import { SessionLink } from "../session/sessionMessage";
import { stopRequests } from "./nativeStopRequests";
import { SubagentBar } from "./SubagentBar";
import { stopOffer } from "./stopOffer";
import { flattenSubagents, type SubagentRow } from "./subagentModel";
import { useSubagentTree } from "./useSubagentTree";

export interface Coordinator {
	ref: string;
	threadId: string;
	title: string;
}

export interface SubagentPanelProps {
	hubId: string;
	/** The subagent's own session ref. */
	ref: string;
	coordinator: Coordinator;
	/** This screen is the one in front (useScreenInFront, ruling 26). */
	inFront: boolean;
	/** The hub takes no message for the subagent: the bar takes the composer's place. */
	barShown: boolean;
	showToast(text: string): void;
	navigation: SessionNavigation & { navigate(name: "StopSubagentSheet", params: object): void };
}

const isResourceNotFound = (error: unknown) => error instanceof WireError && error.evenerErrorInfo === "resourceNotFound";

export function SubagentPanel({ hubId, ref, coordinator, inFront, barShown, showToast, navigation }: SubagentPanelProps) {
	const { client, state, activeProfile } = useConnection();
	const connected = state === "ready" && activeProfile?.id === hubId && client !== null;
	const { tree, snapshot } = useSubagentTree(hubId, coordinator.ref, coordinator.threadId);
	const rows = useMemo(() => (snapshot.tree ? flattenSubagents(snapshot.tree) : []), [snapshot.tree]);
	const row = rows.find((candidate) => candidate.ref === ref) ?? null;
	const requests = stopRequests(hubId);
	const stopRevision = useSyncExternalStore(requests.subscribe, requests.getRevision);

	// The tree reads again when this screen comes to the front, and while it's
	// in front, whenever this subagent's status changes (a turn ending is one),
	// so a stop shows up here (rulings 9 and 26).
	useEffect(() => {
		if (!inFront || !client) return;
		void tree.reload();
		return client.onNotification((notification) => {
			if (notification.method === "thread/status/changed" && notification.params.ref === ref) void tree.reload();
		});
	}, [inFront, client, tree, ref]);

	// The stops you asked for, settled against each new tree, with a toast for
	// each that just stopped. Only the screen in front settles them, so the
	// toast shows where you are, and once.
	useEffect(() => {
		if (!inFront || rows.length === 0) return;
		for (const stopped of requests.reconcile(coordinator.ref, rows)) showToast(`“${stopped.title}” stopped`);
		// A request recorded after the tree already shows the stop settles too.
	}, [inFront, rows, requests, coordinator.ref, showToast, stopRevision]);

	// Whether the coordinator's hub can stop a subagent directly (S6), read
	// without taking the connection's subscription, which follows this
	// subagent's transcript.
	// "unreadable" when the read failed: the bar then asks the coordinator,
	// whose sheet reads it again.
	const [capabilities, setCapabilities] = useState<ThreadCapabilities | "unreadable" | null>(null);
	const [directUnsupported, setDirectUnsupported] = useState(false);
	useEffect(() => {
		// What another connection said doesn't hold on this one.
		setCapabilities(null);
		setDirectUnsupported(false);
		if (!inFront || !connected || !client) return;
		const link = new SessionLink(client, coordinator.ref);
		link.read({ follow: false }).then(
			(session) => setCapabilities(session.capabilities),
			() => setCapabilities("unreadable"),
		);
		return () => link.dispose();
	}, [inFront, connected, client, coordinator.ref]);
	const direct = capabilities !== null && capabilities !== "unreadable" && capabilities.stopSubagent === true && !directUnsupported;

	const stopDirectly = useCallback(
		(target: SubagentRow) => {
			Alert.alert(`Stop “${target.title}”?`, "Anything it started keeps running.", [
				{ text: "Cancel", style: "cancel" },
				{
					text: "Stop",
					style: "destructive",
					onPress: async () => {
						if (!client) return;
						try {
							const response = await client.request("evener/delegate/stop", {
								ref: coordinator.ref,
								threadId: coordinator.threadId,
								delegateId: target.id,
							});
							if (response.outcome === "stopping")
								requests.request(coordinator.ref, target, Date.now(), { direct: true });
							// notRunning: it was already finishing; the tree says how it ended.
							else void tree.reload();
						} catch (error) {
							if (isMethodNotFound(error)) setDirectUnsupported(true);
							else if (isResourceNotFound(error)) void tree.reload();
							else showToast(`Couldn't stop it: ${error instanceof Error ? error.message : String(error)}`);
						}
					},
				},
			]);
		},
		[client, coordinator.ref, coordinator.threadId, requests, tree, showToast],
	);

	if (!barShown) return null;
	const offer = stopOffer({
		row,
		requested: row !== null && requests.view(row) === "requested",
		direct: capabilities === null ? "unknown" : direct ? "yes" : "no",
	});
	return (
		<SubagentBar
			offer={offer}
			onStop={() => {
				if (!row) return;
				if (offer === "stop") stopDirectly(row);
				else navigation.navigate("StopSubagentSheet", { hubId, coordinator, ref: row.ref });
			}}
			onOpenCoordinator={() => returnToSession(navigation, { hubId, ref: coordinator.ref, title: coordinator.title })}
		/>
	);
}
