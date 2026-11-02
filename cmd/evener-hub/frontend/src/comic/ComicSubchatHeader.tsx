import { stableDelegateDisplayStatus } from "@evener/appwire-client";
import { Button, SubagentDot } from "../widgets";
import styles from "./comic.module.css";
import type { ComicDelegateStrip } from "./delegates";

export function ComicSubchatHeader({
  strip,
  expanded,
  onToggle,
  onOpen,
}: {
  strip: ComicDelegateStrip;
  expanded: boolean;
  onToggle(): void;
  onOpen(ref: string): void;
}): React.ReactElement {
  const delegate = strip.entity.row ? strip.entity.row.delegate : strip.entity.stable;
  return (
    <header
      className={styles.subchatHeader}
      data-comic-delegate={strip.key}
      data-comic-child-ref={strip.childRef}
      data-comic-generation={delegate.runGeneration}
    >
      <img
        className={styles.subchatPortrait}
        src={`/webassets/comic/sprites/${strip.persona.character}-neutral.png`}
        alt=""
        aria-hidden="true"
      />
      <div className={styles.subchatIdentity}>
        <strong>Meanwhile… {strip.persona.name}</strong>
        <span>
          {strip.persona.badge} · {strip.ownerRef}
        </span>
        <span>
          <SubagentDot delegate={delegate} now={Date.now()} labeled /> {stableDelegateDisplayStatus(delegate)}
          {strip.entity.stale ? " · State unavailable" : ""}
        </span>
        <span>{strip.childRef}</span>
      </div>
      <div className={styles.controls}>
        <Button
          variant="link"
          aria-expanded={expanded}
          onClick={(event) => {
            event.currentTarget.focus();
            onToggle();
          }}
        >
          {expanded ? "Collapse conversation" : "Expand conversation"}
        </Button>
        <Button variant="link" onClick={() => onOpen(strip.childRef)}>
          Open conversation
        </Button>
      </div>
    </header>
  );
}
