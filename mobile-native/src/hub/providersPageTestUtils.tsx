// The Providers page as the Hub's stack shows it, for the page's tests: the
// page, and a provider's detail pushed over it once the page navigates to
// one, both under the slot the detail reads (providersScreenSlot.tsx). A test
// mounts ProvidersStack where it would mount ProvidersPage; `back` is the
// stack's Back from the detail, through the leave guard the detail sets.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { type ComponentProps, useEffect, useMemo, useState } from "react";
import { act } from "react-test-renderer";
import { backGuard } from "./backGuardTestUtils";
import type { HubRoutes } from "./hubSheetContext";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { ProvidersPage } from "./ProvidersPage";
import { ProvidersScreenSlotProvider, useProviderDetailSlot } from "./providersScreenSlot";

type DetailParams = HubRoutes["ProviderDetail"];

const stack: {
	close: (() => void) | null;
	params: DetailParams | null;
	firstSeen: string | null | undefined;
} = { close: null, params: null, firstSeen: undefined };

/** The provider the slot named when the latest pushed detail first rendered:
 * a detail pushed before its page has published it renders blank at first. */
export function detailFirstSeen(): string | null | undefined {
	return stack.firstSeen;
}

/** The pushed detail page, noting what the slot held at its first render. */
function Pushed(props: ComponentProps<typeof ProviderDetailPage>) {
	const slot = useProviderDetailSlot();
	const [seen] = useState(() => slot?.name ?? null);
	useEffect(() => {
		stack.firstSeen = seen;
	}, [seen]);
	return <ProviderDetailPage {...props} />;
}

/** The detail's route params, or null while no detail is pushed. */
export function detailParams(): DetailParams | null {
	return stack.params;
}

/** Back from the pushed detail: the leave guard asks first when it holds. */
export function back(): void {
	act(() => {
		const guard = backGuard.current;
		if (guard?.prevent) guard.onPrevent({ data: { action: { type: "GO_BACK" } } });
		else stack.close?.();
	});
}

export function ProvidersStack(props: ComponentProps<typeof ProvidersPage>) {
	const [detail, setDetail] = useState<DetailParams | null>(null);
	useEffect(() => {
		stack.params = detail;
		stack.close = () => setDetail(null);
	});
	// A link to a provider (a notice, or a sign-in error) navigates to Providers
	// with `pop` (BoardNotices.tsx's openNotice): it removes a detail pushed
	// over the page, which the detail's leave guard may hold, and only then
	// hands the page its new focus, with the list in front. A new focus here
	// does the same: the page keeps its last applied params until the pop goes.
	const next = props.route.params as HubRoutes["Providers"];
	const [applied, setApplied] = useState(next);
	const nextKey = JSON.stringify(next);
	const appliedKey = JSON.stringify(applied);
	useEffect(() => {
		if (nextKey === appliedKey) return;
		const params = JSON.parse(nextKey) as HubRoutes["Providers"];
		if (params.focus === undefined || !stack.params) {
			setApplied(params);
			return;
		}
		const go = () => {
			setDetail(null);
			setApplied(params);
		};
		const guard = backGuard.current;
		if (guard?.prevent) guard.onPrevent({ data: { action: { type: "NAVIGATE", go } } });
		else go();
	}, [nextKey, appliedKey]);
	const given = props.navigation as unknown as Record<string, unknown> | undefined;
	const navigation = useMemo(
		() =>
			({
				...given,
				navigate: (screen: string, params: DetailParams) => {
					if (screen === "ProviderDetail") setDetail(params);
				},
			}) as unknown as NativeStackScreenProps<HubRoutes, "Providers">["navigation"],
		[given],
	);
	const detailNavigation = useMemo(
		() =>
			({
				canGoBack: () => true,
				goBack: () => setDetail(null),
				// The action a held leave dispatches: a link's pop carries its own
				// step; Back's only removes the detail.
				dispatch: (action: { go?: () => void }) => (action.go ? action.go() : setDetail(null)),
			}) as unknown as NativeStackScreenProps<HubRoutes, "ProviderDetail">["navigation"],
		[],
	);
	return (
		<ProvidersScreenSlotProvider>
			<ProvidersPage {...props} route={{ ...props.route, params: applied }} navigation={navigation} />
			{detail ? (
				<Pushed
					key={detail.name}
					navigation={detailNavigation}
					route={{ key: "detail", name: "ProviderDetail", params: detail }}
				/>
			) : null}
		</ProvidersScreenSlotProvider>
	);
}
