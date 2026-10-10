// Hub > In-app alerts (spec 12; the prototype's hub.js, EV.sheets.alerts):
// which banners show, whether they wait while you read or type, and the one
// Haptics switch. Kept per device (alertPreferences.ts), so the page needs
// no connection and nothing on it is ever off.
import { useSyncExternalStore } from "react";
import type { AlertPreferences } from "../alerts/alertCenter";
import { alertPreferences } from "../alerts/nativeAlertPreferences";
import { Group, GroupedPage, GroupFooter, SwitchRow } from "../sheet/Grouped";

export function AlertsPage() {
	const store = alertPreferences();
	const preferences = useSyncExternalStore(store.subscribe, store.getSnapshot);
	const toggle = (key: keyof AlertPreferences) => (value: boolean) => store.set({ [key]: value });
	return (
		<GroupedPage>
			<Group label="Show a banner when">
				<SwitchRow label="A session fails" value={preferences.failures} onChange={toggle("failures")} />
				<SwitchRow
					label="A session asks a question, needs approval, or needs your reply"
					value={preferences.questions}
					onChange={toggle("questions")}
				/>
				<SwitchRow
					label="A session finishes"
					sub="Finished results always land in Finished on the Board"
					value={preferences.finished}
					onChange={toggle("finished")}
				/>
			</Group>
			<Group>
				<SwitchRow
					label="Hold alerts while reading or typing"
					sub="They show when you leave the document or send"
					value={preferences.hold}
					onChange={toggle("hold")}
				/>
				<SwitchRow label="Haptics" value={preferences.haptics} onChange={toggle("haptics")} />
			</Group>
			<GroupFooter>
				Lock-screen notifications are coming later. Until then, alerts show while Evener is open.
			</GroupFooter>
		</GroupedPage>
	);
}
