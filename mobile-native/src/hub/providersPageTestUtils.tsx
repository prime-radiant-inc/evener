// The Providers page as the Hub's stack shows it, for the page's tests: the
// page, and a provider's detail pushed over it once the page navigates to
// one, both under the slot the detail reads (hubScreenSlot.tsx). A test
// mounts ProvidersStack where it would mount ProvidersPage; `back` is the
// stack's Back from the detail, through the leave guard the detail sets.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { type ComponentProps, useEffect, useMemo, useState } from "react";
import { act } from "react-test-renderer";
import { backGuard } from "./backGuardTestUtils";
import type { HubRoutes } from "./hubSheetContext";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { ProvidersPage } from "./ProvidersPage";
import { ProvidersScreenSlotProvider } from "./hubScreenSlot";

type DetailParams = HubRoutes["ProviderDetail"];

const stack: { close: (() => void) | null; params: DetailParams | null } = { close: null, params: null };

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
				dispatch: () => setDetail(null),
			}) as unknown as NativeStackScreenProps<HubRoutes, "ProviderDetail">["navigation"],
		[],
	);
	return (
		<ProvidersScreenSlotProvider>
			<ProvidersPage {...props} navigation={navigation} />
			{detail ? (
				<ProviderDetailPage
					navigation={detailNavigation}
					route={{ key: "detail", name: "ProviderDetail", params: detail }}
				/>
			) : null}
		</ProvidersScreenSlotProvider>
	);
}
