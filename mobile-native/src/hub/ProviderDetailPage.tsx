// A provider's detail, pushed over the Providers page as a host's detail is
// over Hosts (spec 12; device audit N3). The Providers page underneath builds
// it and publishes it (providersScreenSlot.tsx); this page shows it while it
// is the one the route names, asks before Back discards a pasted key, and
// goes back when the Providers page no longer has the provider selected (it
// was removed, a write moved it, or its sign-in started).
import { usePreventRemove } from "@react-navigation/native";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useRef } from "react";
import type { HubRoutes } from "./hubSheetContext";
import { useProviderDetailSlot } from "./providersScreenSlot";

export function ProviderDetailPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "ProviderDetail">) {
	const { hubId, name } = route.params;
	const slot = useProviderDetailSlot();
	// Only the detail the route names, for the hub it was opened for: after a
	// hub switch, nothing shows rather than another provider's detail.
	const current = slot !== null && slot.hubId === hubId && slot.name === name ? slot : null;

	// Having shown its provider, the page goes back once the Providers page no
	// longer publishes it. Nothing swaps the provider under a pushed detail: the
	// list and Add are covered, an edit keeps the name, and a link pops the
	// detail before the page opens another (openNotice's `pop`).
	const shown = useRef(false);
	useEffect(() => {
		if (current) shown.current = true;
		else if (shown.current && navigation.canGoBack()) navigation.goBack();
	}, [current, navigation]);

	// Back and the edge swipe ask before a pasted key goes, and wait out its
	// save, as the paste sheet's own Cancel does (spec 6).
	usePreventRemove(current?.guarded ?? false, ({ data }) => {
		current?.leave(() => navigation.dispatch(data.action));
	});

	// Leaving hands the selection back, so the Providers page drops whatever
	// the detail had open (a draft key, a check's failure).
	const latest = useRef(current);
	useEffect(() => {
		latest.current = current;
	});
	useEffect(() => () => latest.current?.onGone(), []);

	return current ? current.detail : null;
}
