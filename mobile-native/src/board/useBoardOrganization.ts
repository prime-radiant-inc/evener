// The Board's one NavigationActions (ruling 16). It checks whatever change
// the hub's organization journal holds (organizationCheck.ts), settles it
// whenever the Board is focused and connected, as usePinNavigation does, and
// never shows the journal's own error text: while a change is unresolved,
// `ready` is false and the Board hides its organization actions.
import { useIsFocused } from "@react-navigation/native";
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useSyncExternalStore,
} from "react";
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { useConnection } from "../ConnectionProvider";
import { organizationJournal } from "../nativeOrganization";
import { NavigationActions } from "../navigationActions";
import { NavigationPages } from "../navigationPages";
import { checkOrganizationChange, organizationFree } from "./organizationCheck";

const noSnapshot = () => null;
const noSubscription = () => () => {};
const sectionKey = (section: NavigationPinSectionDescriptor) => section.id;

export interface BoardOrganization {
	/** Null unless this hub's connection is ready. */
	actions: NavigationActions | null;
	state: ReturnType<NavigationActions["getSnapshot"]> | null;
	/** The journal can take a change now. */
	ready: boolean;
}

export function useBoardOrganization(hubId: string): BoardOrganization {
	const { client, activeProfile, state } = useConnection();
	const focused = useIsFocused();
	const belongs = activeProfile?.id === hubId;
	const ready = belongs && !!client && state === "ready";
	const journal = useMemo(() => organizationJournal(hubId), [hubId]);
	const pinPages = useMemo(
		() =>
			client && belongs
				? new NavigationPages<NavigationPinSectionDescriptor>(
						client,
						{ resource: "pin_catalog" },
						"pin_sections",
						sectionKey,
					)
				: null,
		[client, belongs],
	);
	const binding = useMemo(
		() => ({ client, pinPages, ready, focused }),
		[client, pinPages, ready, focused],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const isCurrent = useCallback(
		() => owner.current === binding && binding.ready && binding.focused,
		[binding],
	);
	const actions = useMemo(() => {
		if (!client || !pinPages) return null;
		return new NavigationActions(
			client,
			async (_receipt, checkpoint) => {
				if (!checkpoint) throw Error("The organization change was not saved.");
				if (!(await checkOrganizationChange(client, pinPages, checkpoint, isCurrent, true)))
					throw Error("The hub doesn't show the change yet.");
			},
			isCurrent,
			async (checkpoint) => {
				// A read that succeeds settles the journal: the Board then shows
				// wherever the hub has the row, with nothing to review.
				if (checkpoint) await checkOrganizationChange(client, pinPages, checkpoint, isCurrent, false);
			},
			journal,
		);
	}, [client, pinPages, journal, isCurrent]);
	const actionState = useSyncExternalStore(
		actions?.subscribe ?? noSubscription,
		actions?.getSnapshot ?? noSnapshot,
	);
	useEffect(() => {
		if (ready && focused) void actions?.reconcile();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
			pinPages?.cancel();
		};
	}, [actions, ready, focused, pinPages, binding]);
	return {
		actions: ready ? actions : null,
		state: actionState,
		ready: ready && organizationFree(actionState),
	};
}
