import { Button, IconButton } from "../../widgets";
import { iPhoneHost, tellIPhoneHost } from "../iPhoneHost";
import { useDismissNavigationChrome } from "../navigationChrome";
import { navigate } from "../routing";
import { useIsMobile } from "../useIsMobile";
import { GearIcon, SearchIcon } from "./railIcons";

export function useOpenRailDestination() {
  const dismiss = useDismissNavigationChrome();
  return (action: "hubs" | "settings") => {
    if (action === "hubs") tellIPhoneHost({ type: "hubMenu" });
    else {
      dismiss?.();
      navigate("/settings");
    }
  };
}

export function RailNavigationActions() {
  const isMobile = useIsMobile();
  const openRailDestination = useOpenRailDestination();
  return (
    <>
      {iPhoneHost() && (
        <Button size="sm" variant="quiet" aria-label="Hubs on this iPhone" onClick={() => openRailDestination("hubs")}>
          Hubs
        </Button>
      )}
      <IconButton
        data-testid="rail-settings"
        label="Settings"
        icon={<GearIcon />}
        variant="quiet"
        size="md"
        onClick={() => openRailDestination("settings")}
      />
      {!isMobile && (
        <IconButton
          data-testid="rail-search"
          data-search-trigger="true"
          label="Search"
          icon={<SearchIcon />}
          variant="quiet"
          size="md"
        />
      )}
    </>
  );
}
