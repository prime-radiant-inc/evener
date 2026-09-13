import { useIsFocused } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import { Alert } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import { createConversationService } from "../../mobile/src/services/conversation";
import { useConnection } from "./ConnectionProvider";
import { ForkEditor } from "./ForkEditor";
import { ForkActions } from "./forkActions";
import type { ForkChild } from "./forkCheckpointRepository";
import { drafts } from "./nativeDrafts";
import { locations } from "./nativeLocation";
import { forkJournal } from "./nativeOrganization";
import type { Routes } from "./screens";
import { Copy, styles, useColors } from "./ui";

const noSnapshot = () => null;
const noSubscription = () => () => {};

export function ForkScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "Fork">) {
	const { hubId, ref, title, instanceId, entryIndex, preview } = route.params;
	const { client, activeProfile, state, retry } = useConnection();
	const focused = useIsFocused();
	const belongs = activeProfile?.id === hubId;
	const ready = belongs && !!client && state === "ready";
	const colors = useColors();
	const service = useMemo(
		() => (client && belongs ? createConversationService(client) : null),
		[client, belongs],
	);
	const repository = useMemo(() => forkJournal(hubId, ref), [hubId, ref]);
	const binding = useMemo(
		() => ({
			service,
			repository,
			hubId,
			ref,
			instanceId,
			entryIndex,
			ready,
			focused,
		}),
		[service, repository, hubId, ref, instanceId, entryIndex, ready, focused],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const isCurrent = useCallback(
		() => owner.current === binding && binding.ready && binding.focused,
		[binding],
	);
	const source = useRef<{
		owner: typeof binding;
		conversation: MobileConversation;
	} | null>(null);
	const reading = useRef<typeof binding | null>(null);
	const [readState, setReadState] = useState<{
		owner: typeof binding;
		pending: boolean;
		error: string | null;
	} | null>(null);
	const actions = useMemo(
		() =>
			service
				? new ForkActions(repository, service, drafts, hubId, isCurrent, () =>
						source.current?.owner === binding &&
						source.current.conversation.capabilities?.forkFromTurn
							? (source.current.conversation.instanceId ?? null)
							: null,
					)
				: null,
		[service, repository, hubId, isCurrent, binding],
	);
	const action = useSyncExternalStore(
		actions?.subscribe ?? noSubscription,
		actions?.getSnapshot ?? noSnapshot,
	);
	const readSource = useCallback(async () => {
		if (!service || !isCurrent() || reading.current === binding) return false;
		reading.current = binding;
		source.current = null;
		setReadState({ owner: binding, pending: true, error: null });
		try {
			const value = await service.readProjection(ref);
			if (!isCurrent()) return false;
			source.current = { owner: binding, conversation: value.conversation };
			const valid =
				value.conversation.instanceId === instanceId &&
				value.conversation.capabilities?.forkFromTurn === true;
			setReadState({
				owner: binding,
				pending: false,
				error:
					value.conversation.instanceId !== instanceId
						? "The source session changed. Return to it and select the message again."
						: !value.conversation.capabilities?.forkFromTurn
							? "This session cannot be forked here."
							: null,
			});
			return valid;
		} catch {
			if (isCurrent())
				setReadState({
					owner: binding,
					pending: false,
					error:
						"The source session could not be checked. Retry before creating a fork.",
				});
			return false;
		} finally {
			if (reading.current === binding) reading.current = null;
		}
	}, [service, isCurrent, binding, ref, instanceId]);
	useEffect(() => {
		if (ready && focused) void readSource();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
			service?.close();
		};
	}, [ready, focused, readSource, binding, actions, service]);
	function open(child: ForkChild | null) {
		if (!child || !isCurrent()) return;
		actions?.finish(locations, (destination) =>
			navigation.replace("Conversation", {
				hubId,
				ref: destination.ref,
				title: destination.title,
			}),
		);
	}
	async function create() {
		if (!actions || actions.getSnapshot().pending || !isCurrent()) return;
		if (await readSource())
			open(await actions.create({ instanceId, entryIndex, preview }));
	}
	function allowAnother() {
		const saved = actions?.getSnapshot().checkpoint;
		if (!saved || saved.child || !isCurrent()) return;
		Alert.alert(
			"Allow another fork?",
			"The previous request may already have created a session. Continuing lets you make another request, which may create a duplicate.",
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Allow another fork",
					onPress: () => {
						if (isCurrent()) actions?.discardUnknown(saved);
					},
				},
			],
		);
	}
	const checked = readState?.owner === binding ? readState : null;
	return (
		<SafeAreaView
			edges={["bottom", "left", "right"]}
			style={[styles.fill, { backgroundColor: colors.background }]}
		>
			{!belongs ? (
				<Copy>
					This hub is no longer selected. Return to Hubs to reconnect.
				</Copy>
			) : (
				<ForkEditor
					title={title || "Fork session"}
					hubName={activeProfile?.name ?? "Hub"}
					preview={action?.checkpoint?.target.preview ?? preview}
					connected={ready && focused}
					canCreate={
						!!checked && !checked.pending && !checked.error && !!actions
					}
					pending={!!action?.pending || !!checked?.pending}
					hasCheckpoint={!!action?.checkpoint}
					hasChild={!!action?.checkpoint?.child}
					storageUnavailable={!!action?.storageUnavailable}
					error={action?.error ?? null}
					sourceError={
						action?.checkpoint?.child ? null : (checked?.error ?? null)
					}
					create={() => void create()}
					openChild={() => {
						open(actions?.openChild() ?? null);
					}}
					retry={() => {
						actions?.retryStorage();
						if (!ready) retry();
						else void readSource();
					}}
					browseSessions={() => navigation.popTo("Sessions")}
					allowAnother={allowAnother}
					close={() => navigation.goBack()}
				/>
			)}
		</SafeAreaView>
	);
}
