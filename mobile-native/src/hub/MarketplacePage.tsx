// A marketplace's own page in the Hub (audit M8): pushed from the Plugins
// page's Marketplaces or Browse list, so the edge swipe and the nav Back
// return to that list. Everything it writes through belongs to the Plugins
// page underneath, which publishes it (pluginsScreenSlot.tsx); a publication
// for another hub, or none at all, means the Plugins page is gone, and so
// does this page.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useCallback, useEffect } from "react";
import { MarketplaceDetail } from "../MarketplaceBrowser";
import { GroupedPage } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import type { HubRoutes } from "./hubSheetContext";
import { usePluginsScreenSlot } from "./pluginsScreenSlot";

export function MarketplacePage({ route, navigation }: NativeStackScreenProps<HubRoutes, "Marketplace">) {
	const { hubId, name, segment } = route.params;
	const slot = usePluginsScreenSlot();
	const owned = slot?.hubId === hubId ? slot : null;
	const goBack = useCallback(() => navigation.goBack(), [navigation]);
	useEffect(() => {
		if (!owned) goBack();
	}, [owned, goBack]);
	if (!owned) return null;
	return (
		<GroupedPage>
			<SheetStatus />
			<MarketplaceDetail
				segment={segment}
				name={name}
				client={owned.client}
				hubName={owned.hubName}
				installed={owned.installed}
				marketplaces={owned.marketplaces}
				gate={owned.gate}
				ready={owned.ready}
				canUseConnection={owned.canUseConnection}
				appliedRemovalNames={owned.appliedRemovalNames}
				onAppliedRemoval={owned.onAppliedRemoval}
				onRemovedMarketplace={owned.onRemovedMarketplace}
				onOpenPlugin={(target) => navigation.popTo("Plugins", { hubId, focus: target })}
				onGone={goBack}
			/>
		</GroupedPage>
	);
}
