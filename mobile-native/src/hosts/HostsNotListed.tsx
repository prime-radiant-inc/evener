// A host page before the hub's first list of hosts: connecting, and why a
// refused read failed. The page keeps asking every poll.
import { GroupedPage, GroupFooter } from "../sheet/Grouped";
import { FirstLoad, SheetStatus } from "../sheet/SheetStatus";

export function HostsNotListed({ hubName, error }: { hubName: string; error: string | null }) {
	return (
		<GroupedPage>
			<SheetStatus />
			<FirstLoad hubName={hubName} label="Loading hosts" />
			{error ? <GroupFooter tone="danger">{`Couldn't list this hub's hosts: ${error}`}</GroupFooter> : null}
		</GroupedPage>
	);
}
