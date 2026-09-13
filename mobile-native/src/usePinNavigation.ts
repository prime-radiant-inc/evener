import { useIsFocused } from "@react-navigation/native";
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import type { NavigationPinSectionDescriptor } from "../../appwire-client/typescript/types.gen";
import { useConnection } from "./ConnectionProvider";
import { organizationJournal } from "./nativeOrganization";
import { NavigationActions } from "./navigationActions";
import { NavigationPages } from "./navigationPages";
import { refreshPinNavigation } from "./pinNavigation";

const noSnapshot = () => null;
const noSubscription = () => () => {};
const sectionKey = (section: NavigationPinSectionDescriptor) => section.id;

export function usePinNavigation(
	hubId: string,
	sessionRef?: string,
	sectionId?: string,
) {
	const { client, activeProfile, state, retry } = useConnection();
	const focused = useIsFocused();
	const belongs = activeProfile?.id === hubId;
	const ready = belongs && !!client && state === "ready";
	const journal = useMemo(() => organizationJournal(hubId), [hubId]);
	const pages = useMemo(
		() =>
			client && activeProfile?.id === hubId
				? new NavigationPages<NavigationPinSectionDescriptor>(
						client,
						{ resource: "pin_catalog" },
						"pin_sections",
						sectionKey,
					)
				: null,
		[client, activeProfile?.id, hubId],
	);
	const binding = useMemo(
		() => ({ client, pages, ready, focused, hubId, sessionRef, sectionId }),
		[client, pages, ready, focused, hubId, sessionRef, sectionId],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const isCurrent = useCallback(
		() => owner.current === binding && binding.ready && binding.focused,
		[binding],
	);
	const [readback, setReadback] = useState<{
		owner: typeof binding;
		value: Awaited<ReturnType<typeof refreshPinNavigation>>;
	} | null>(null);
	const actions = useMemo(() => {
		if (!client || !pages) return null;
		const read = async (
			checkpoint?: Parameters<typeof refreshPinNavigation>[2]["checkpoint"],
			confirmReceipt = false,
		) => {
			const value = await refreshPinNavigation(client, pages, {
				checkpoint,
				sessionRef,
				sectionId,
				current: isCurrent,
				confirmReceipt,
			});
			if (isCurrent()) setReadback({ owner: binding, value });
		};
		return new NavigationActions(
			client,
			async (_receipt, checkpoint) => {
				if (!checkpoint) throw Error("Pin recovery was not saved.");
				await read(checkpoint, true);
			},
			isCurrent,
			(checkpoint) => read(checkpoint),
			journal,
		);
	}, [client, pages, binding, journal, isCurrent, sessionRef, sectionId]);
	const page = useSyncExternalStore(
		pages?.subscribe ?? noSubscription,
		pages?.getSnapshot ?? noSnapshot,
	);
	const action = useSyncExternalStore(
		actions?.subscribe ?? noSubscription,
		actions?.getSnapshot ?? noSnapshot,
	);
	useEffect(() => pages?.watch(), [pages]);
	useEffect(() => {
		if (ready && focused) void actions?.reconcile();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
			pages?.cancel();
		};
	}, [actions, ready, focused, pages, binding]);
	const observed =
		readback?.owner.pages === pages &&
		readback.owner.hubId === hubId &&
		readback.owner.sessionRef === sessionRef &&
		readback.owner.sectionId === sectionId
			? readback.value
			: null;
	const confirmed = !!observed && readback?.owner === binding;
	return {
		client,
		activeProfile,
		belongs,
		ready,
		focused,
		retry,
		pages,
		page,
		actions,
		action,
		observed,
		confirmed,
		isCurrent,
	};
}
