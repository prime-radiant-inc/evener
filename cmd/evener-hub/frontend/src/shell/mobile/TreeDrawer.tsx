import { useMemo } from "react";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { IconButton } from "../../widgets";

function SessionsIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M2 4 H14 M2 8 H14 M2 12 H14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

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
