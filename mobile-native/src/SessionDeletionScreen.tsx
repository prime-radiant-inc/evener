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
import { useConnection } from "./ConnectionProvider";
import { locations } from "./nativeLocation";
import { organizationJournal } from "./nativeOrganization";
import type { NavigationActionCheckpoint } from "./navigationActionRepository";
import { NavigationActions } from "./navigationActions";
import { SessionDeletionEditor } from "./SessionDeletionEditor";
import type { Routes } from "./screens";
import { readSessionDeletion } from "./sessionDeletionNavigation";

const noSnapshot = () => null;
const noSubscription = () => () => {};
type Readback = Awaited<ReturnType<typeof readSessionDeletion>>;

export function SessionDeletionScreen({
	route,
	navigation,
}: NativeStackScreenProps<Routes, "SessionDeletion">) {
	const { hubId, ref, title } = route.params;
	const { client, activeProfile, state, retry } = useConnection();
	const focused = useIsFocused();
	const ready = activeProfile?.id === hubId && !!client && state === "ready";
	const binding = useMemo(
		() => ({ client, hubId, ref, ready, focused }),
		[client, hubId, ref, ready, focused],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const current = useCallback(
		() => owner.current === binding && binding.ready && binding.focused,
		[binding],
	);
	const journal = useMemo(() => organizationJournal(hubId), [hubId]);
	const [readback, setReadback] = useState<{
		owner: typeof binding;
		value: Readback;
	} | null>(null);
	const latest = useRef<typeof readback>(null);
	const [problem, setProblem] = useState<{
		owner: typeof binding;
		error: string;
	} | null>(null);
	const [checking, setChecking] = useState<typeof binding | null>(null);
	const checkingRef = useRef<typeof binding | null>(null);
	const actions = useMemo(() => {
		if (!client || activeProfile?.id !== hubId) return null;
		const read = async (
			checkpoint?: NavigationActionCheckpoint,
			confirmReceipt = false,
		) => {
			const value = await readSessionDeletion(
				client,
				ref,
				checkpoint,
				current,
				confirmReceipt,
			);
			if (!current()) throw Error("The session screen changed.");
			latest.current = { owner: binding, value };
			setReadback(latest.current);
			if (!value.settled)
				throw Error("The previous deletion is still unconfirmed.");
		};
		return new NavigationActions(
			client,
			(_receipt, checkpoint) => read(checkpoint, true),
			current,
			read,
			journal,
		);
	}, [client, activeProfile?.id, hubId, ref, binding, current, journal]);
	const action = useSyncExternalStore(
		actions?.subscribe ?? noSubscription,
		actions?.getSnapshot ?? noSnapshot,
	);
	useEffect(() => {
		if (ready && focused) void actions?.reconcile();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
		};
	}, [actions, ready, focused, binding]);
	const observed =
		readback?.owner.hubId === hubId && readback.owner.ref === ref
			? readback.value
			: null;
	const confirmed = readback?.owner === binding;
	const busy = !!action?.pending || checking === binding;
	const enabled =
		ready &&
		focused &&
		confirmed &&
		!busy &&
		!action?.uncertain &&
		!action?.storageUnavailable;
	function fail(error: unknown) {
		if (current())
			setProblem({
				owner: binding,
				error:
					error instanceof Error
						? error.message
						: "The session could not be checked.",
			});
	}
	function refresh() {
		setProblem(null);
		if (ready) void actions?.reconcile();
		else retry();
	}
	function openSessions() {
		if (owner.current !== binding || !focused || activeProfile?.id !== hubId)
			return;
		try {
			locations.save({ hubId });
			navigation.popTo("Sessions");
		} catch {
			fail(
				Error(
					"The destination could not be saved. Try opening Sessions again.",
				),
			);
		}
	}
	async function remove(expected: Readback) {
		if (!current() || !client || !actions || checkingRef.current === binding)
			return;
		checkingRef.current = binding;
		setChecking(binding);
		setProblem(null);
		try {
			const value = await readSessionDeletion(client, ref, undefined, current);
			if (!current()) return;
			latest.current = { owner: binding, value };
			setReadback(latest.current);
			if (
				!value.eligible ||
				value.instanceId !== expected.instanceId ||
				value.generationId !== expected.generationId ||
				value.title !== expected.title
			)
				throw Error(
					"The session changed while confirmation was open. Review its current state before deleting.",
				);
			await actions.deleteSession({ ref });
			const result = actions.getSnapshot();
			if (
				current() &&
				!result.pending &&
				!result.uncertain &&
				!result.error &&
				latest.current?.owner === binding &&
				latest.current.value.missing
			)
				openSessions();
		} catch (error) {
			fail(error);
		} finally {
			if (checkingRef.current === binding) checkingRef.current = null;
			setChecking((previous) => (previous === binding ? null : previous));
		}
	}
	function requestDelete() {
		if (!enabled || !observed?.eligible || !actions || !current()) return;
		const expected = observed;
		Alert.alert(
			`Delete ${observed.title || title}?`,
			"This permanently removes this session’s saved history from the hub. Other sessions and your local unsent draft are kept.",
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Delete saved session",
					style: "destructive",
					onPress: () => {
						void remove(expected);
					},
				},
			],
		);
	}
	const checkpoint = action?.recovery;
	const retryAvailable =
		confirmed &&
		ready &&
		!!observed &&
		!observed.missing &&
		checkpoint?.operation.kind === "deleteSession" &&
		checkpoint.operation.params.ref === ref &&
		checkpoint.receipt === null;
	function allowRetry() {
		if (!retryAvailable || !checkpoint || !actions || !current()) return;
		Alert.alert(
			"Allow another deletion attempt?",
			"The earlier request is still unconfirmed. This only clears its local recovery record; it cannot cancel a request already sent. A new deletion will require confirmation.",
			[
				{ text: "Keep checking", style: "cancel" },
				{
					text: "Allow another attempt",
					onPress: () => {
						if (current() && actions.allowDeletionRetry(checkpoint)) refresh();
					},
				},
			],
		);
	}
	return (
		<SessionDeletionEditor
			title={observed?.title || title}
			hubName={
				activeProfile?.id === hubId ? activeProfile.name : "Disconnected hub"
			}
			ready={ready}
			busy={busy}
			eligible={!!observed?.eligible}
			canDelete={!!observed?.eligible && enabled}
			missing={!!observed?.missing}
			lastKnown={!confirmed || !ready}
			skippedReason={observed?.skippedReason ?? null}
			error={
				(problem?.owner === binding ? problem.error : null) ??
				action?.error ??
				null
			}
			uncertain={!!action?.uncertain}
			retryAvailable={retryAvailable}
			refresh={refresh}
			requestDelete={requestDelete}
			allowRetry={allowRetry}
			openSessions={openSessions}
			close={() => navigation.goBack()}
		/>
	);
}
