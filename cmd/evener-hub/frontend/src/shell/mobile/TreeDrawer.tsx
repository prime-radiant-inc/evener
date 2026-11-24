import { useMemo } from "react";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { IconButton } from "../../widgets";
import { SessionsIcon } from "../rail/railIcons";

export interface TreeDrawerProps {
  onToggle: () => void;
}

export function TreeDrawer({ onToggle }: TreeDrawerProps) {
  const navigation = useNavigationStore();
  const needsYou = useMemo(() => selectNeedsYouCount(navigation), [navigation]);

  return (
    <IconButton
      label={needsYou > 0 ? `Sessions (${needsYou} need attention)` : "Sessions"}
      icon={<SessionsIcon />}
      variant="quiet"
      onClick={onToggle}
      badge={needsYou > 0 ? { count: needsYou, tone: "attention" } : undefined}
    />
  );
}
