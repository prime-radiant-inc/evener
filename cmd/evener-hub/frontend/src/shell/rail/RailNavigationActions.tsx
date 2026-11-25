import { IconButton } from "../../widgets";
import { navigate } from "../routing";
import { GearIcon, SearchIcon } from "./railIcons";

export function RailNavigationActions() {
  return (
    <>
      <IconButton
        data-testid="rail-settings"
        label="Settings"
        icon={<GearIcon />}
        variant="quiet"
        size="md"
        onClick={() => navigate("/settings")}
      />
      <IconButton
        data-testid="rail-search"
        data-search-trigger="true"
        label="Search"
        icon={<SearchIcon />}
        variant="quiet"
        size="md"
      />
    </>
  );
}
