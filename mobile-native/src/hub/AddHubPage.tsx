// Adding a hub from inside the Hub sheet. Saving selects the new hub, and the
// sheet closes onto its Board by itself (useClosesOnHubChange), so connecting
// here navigates nowhere and the sheet's close is the only step taken.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { AddHub } from "../hubs/AddHub";
import type { HubRoutes } from "./hubSheetContext";

function closedBySelection() {}

export function AddHubPage({ route }: NativeStackScreenProps<HubRoutes, "AddHub">) {
	return <AddHub how={route.params.how} onConnected={closedBySelection} />;
}
