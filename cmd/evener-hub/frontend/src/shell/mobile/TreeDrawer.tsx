import { useMemo } from "react";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { Badge, IconButton } from "../../widgets";
import styles from "./treedrawer.module.css";

function SessionsIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M2 4 H14 M2 8 H14 M2 12 H14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

export interface TreeDrawerProps {
  onOpen: () => void;
}

export function TreeDrawer({ onOpen }: TreeDrawerProps) {
  const navigation = useNavigationStore();
  const needsYou = useMemo(() => selectNeedsYouCount(navigation), [navigation]);

  return (
    <span className={styles.triggerWrap}>
      <IconButton label="Sessions" icon={<SessionsIcon />} variant="quiet" onClick={onOpen} />
      {needsYou > 0 && (
        <span className={styles.badgeOverlay}>
          <Badge count={needsYou} tone="attention" />
        </span>
      )}
    </span>
  );
}
