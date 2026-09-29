// What a subagent's own session adds to the Session (spec 9, rulings 10 and
// 30): its row in the coordinator's shared tree, read again while this screen
// is in front; the stop requests you sent, settled against that tree with a
// toast; and, while the hub takes no message for the subagent, the bar where
// its composer would be. A stop goes to the hub directly when the
// coordinator's hub can make one (S6), and is asked of the coordinator
// otherwise.
import { WireError } from "@evener/appwire-client";
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { Alert } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { SessionLink } from "../session/sessionMessage";
import { isMethodNotFound } from "../wireErrors";
import { returnToSession, type SessionNavigation } from "../session/returnToSession";
import { stopRequests } from "./nativeStopRequests";
import { SubagentBar } from "./SubagentBar";
import { stopOffer } from "./stopOffer";
import { flattenSubagents, type SubagentRow } from "./subagentModel";
import { useCoordinatorState } from "./useCoordinatorState";
import { useSubagentTree } from "./useSubagentTree";
import { destructiveButton } from "../haptics";

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
	/** This subagent's row in its coordinator's tree, as the tree reads it, for
	 * the screen's state line to time its run as the row does. */
	onRow?(row: SubagentRow | null): void;
	navigation: SessionNavigation & { navigate(name: "StopSubagentSheet", params: object): void };
}

const isResourceNotFound = (error: unknown) =>
	error instanceof WireError && error.evenerErrorInfo === "resourceNotFound";

export function SubagentPanel({
	hubId,
	ref,
	coordinator,
	inFront,
	barShown,
	showToast,
	onRow,
	navigation,
}: SubagentPanelProps) {
	const { client, state, activeProfile } = useConnection();
	const connected = state === "ready" && activeProfile?.id === hubId && client !== null;
	// The coordinator's state, read while this screen is in front, without
	// taking the connection's subscription, which the transcript under this
	// screen follows. Its thread id is the tree's: a coordinator that
	// restarted since the screen opened runs under a new thread, and
	// ActivityList refuses a tree whose root isn't the thread asked for.
	const coordinatorState = useCoordinatorState(inFront && connected ? client : null, coordinator.ref);
	const treeThreadId =
		coordinatorState && coordinatorState !== "unreadable" ? coordinatorState.threadId : coordinator.threadId;
	const { tree, snapshot } = useSubagentTree(hubId, coordinator.ref, treeThreadId);
	const rows = useMemo(() => (snapshot.tree ? flattenSubagents(snapshot.tree) : []), [snapshot.tree]);
	const row = rows.find((candidate) => candidate.ref === ref) ?? null;
	useEffect(() => {
		onRow?.(row);
		return () => onRow?.(null);
	}, [onRow, row]);
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
	// while this screen is in front. "unreadable" when the read failed: the
	// bar then asks the coordinator, whose sheet reads it again. A hub that
	// didn't know the direct stop is asked again on the next connection.
	const [directUnsupported, setDirectUnsupported] = useState(false);
	useEffect(() => setDirectUnsupported(false), [client]);
	const direct =
		coordinatorState !== null &&
		coordinatorState !== "unreadable" &&
		coordinatorState.capabilities.stopSubagent === true &&
		!directUnsupported;

	const stopDirectly = useCallback(
		(target: SubagentRow) => {
			Alert.alert(`Stop “${target.title}”?`, "Anything it started keeps running.", [
				{ text: "Cancel", style: "cancel" },
				destructiveButton("Stop", async () => {
					if (!client) return;
					try {
						// The stop names the coordinator's thread as it reads now: a
						// coordinator that restarted since the screen opened runs
						// under a new one. Read without following, as the bar's
						// capability read is.
						const link = new SessionLink(client, coordinator.ref);
						const now = await link.read({ follow: false }).finally(() => link.dispose());
						const response = await client.request("evener/delegate/stop", {
							ref: coordinator.ref,
							threadId: now.threadId,
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
				}),
			]);
		},
		[client, coordinator.ref, requests, tree, showToast],
	);

	if (!barShown) return null;
	const offer = stopOffer({
		row,
		requested: row !== null && requests.view(row) === "requested",
		direct: coordinatorState === null ? "unknown" : direct ? "yes" : "no",
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
