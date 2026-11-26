import { Button, IconButton } from "../../widgets";
import { iPhoneHost, tellIPhoneHost } from "../iPhoneHost";
import { navigate } from "../routing";
import { useIsMobile } from "../useIsMobile";
import { GearIcon, SearchIcon } from "./railIcons";

export function RailNavigationActions() {
  const isMobile = useIsMobile();
  return (
    <>
      {iPhoneHost() && (
        <Button
          size="sm"
          variant="quiet"
          aria-label="Hubs on this iPhone"
          onClick={() => tellIPhoneHost({ type: "hubMenu" })}
        >
          Hubs
        </Button>
      )}
      <IconButton
        data-testid="rail-settings"
        label="Settings"
        icon={<GearIcon />}
        variant="quiet"
        size="md"
        onClick={() => navigate("/settings")}
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
