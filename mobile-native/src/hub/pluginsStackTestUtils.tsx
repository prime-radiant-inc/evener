// The Plugins page as the Hub's stack holds it, for tests: the page, and a
// marketplace's page pushed over it with the Plugins page still mounted
// underneath, sharing the slot the Hub sheet provides. `push("Marketplace")`
// pushes, the harness's "Back to Plugins" control and the marketplace page's
// goBack pop, and `popTo("Plugins", params)` pops with the params the Plugins
// page then reads. Every other navigation call reaches the test's own mocks.
import { createElement, type ComponentProps, useMemo, useState } from "react";
import { MarketplacePage } from "./MarketplacePage";
import { PluginsPage } from "./PluginsPage";
import type { HubRoutes } from "./hubSheetContext";
import { PluginsScreenSlotProvider } from "./pluginsScreenSlot";

type PluginsProps = ComponentProps<typeof PluginsPage>;
type MarketplaceProps = ComponentProps<typeof MarketplacePage>;

export function PluginsStack({ route, navigation }: PluginsProps) {
	const [pushed, setPushed] = useState<HubRoutes["Marketplace"] | null>(null);
	const [focus, setFocus] = useState<HubRoutes["Plugins"]["focus"]>(undefined);
	const pluginsNavigation = useMemo(
		() =>
			({
				...navigation,
				push: (name: string, params: unknown) => {
					if (name === "Marketplace") setPushed(params as HubRoutes["Marketplace"]);
					else (navigation.push as (name: string, params: unknown) => void)(name, params);
				},
				setParams: (params: Partial<HubRoutes["Plugins"]>) => {
					if ("focus" in params) setFocus(params.focus);
					navigation.setParams(params);
				},
			}) as PluginsProps["navigation"],
		[navigation],
	);
	const marketplaceNavigation = useMemo(
		() =>
			({
				goBack: () => setPushed(null),
				popTo: (_name: "Plugins", params: HubRoutes["Plugins"]) => {
					setPushed(null);
					setFocus(params.focus);
				},
			}) as unknown as MarketplaceProps["navigation"],
		[],
	);
	const params = focus ? { ...route.params, focus } : route.params;
	return (
		<PluginsScreenSlotProvider>
			<PluginsPage route={{ ...route, params }} navigation={pluginsNavigation} />
			{pushed ? (
				<>
					<MarketplacePage
						route={{ key: "Marketplace", name: "Marketplace", params: pushed }}
						navigation={marketplaceNavigation}
					/>
					{createElement("HarnessBack", { accessibilityLabel: "Back to Plugins", onPress: () => setPushed(null) })}
				</>
			) : null}
		</PluginsScreenSlotProvider>
	);
}
