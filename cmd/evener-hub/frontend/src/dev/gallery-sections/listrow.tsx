import { Button } from "../../widgets/button";
import { Chip } from "../../widgets/chip";
import { ListRow } from "../../widgets/listrow";
import { StatusDot } from "../../widgets/statusdot";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./listrow.module.css";

export default function ListRowGallerySection() {
  return (
    <section>
      <h2>ListRow</h2>
      <ThemeFlip>
        <div className={styles.columns}>
          <ul className={styles.list} aria-label="collection rows">
            <li>
              <ListRow
                leading={<StatusDot state="working" />}
                label="superpowers"
                chips={<Chip>auto-upgrade</Chip>}
                meta="@ obra · v1.2.0"
                onSelect={() => {}}
              />
            </li>
            <li>
              <ListRow
                label="claude-plugins-official"
                meta="github.com/anthropics/claude-plugins"
                onSelect={() => {}}
              />
            </li>
            <li>
              <ListRow label="OpenRouter (offline)" onSelect={() => {}} disabled />
            </li>
            <li>
              <ListRow
                leading={<StatusDot state="idle" />}
                label="codex-work"
                chips={<Chip>from environment</Chip>}
                meta="device sign-in · host"
                action={
                  <Button variant="quiet" size="sm">
                    Sign in on host
                  </Button>
                }
              />
            </li>
            <li>
              <ListRow
                label="filesystem — stdio"
                chips={<Chip tone="alive">available</Chip>}
                meta="read-only: no onSelect"
              />
            </li>
          </ul>
          <ul className={styles.list} aria-label="plain rows">
            <li>
              <ListRow variant="plain" label="Pinned" onSelect={() => {}} />
            </li>
            <li>
              <ListRow
                variant="plain"
                leading={<span aria-hidden="true">$</span>}
                label="npm run build"
                meta="running · 2m"
                onSelect={() => {}}
              />
            </li>
            <li>
              <ListRow variant="plain" label="New section…" onSelect={() => {}} disabled />
            </li>
            <li>
              <ListRow
                variant="plain"
                leading={<span aria-hidden="true">⌘</span>}
                label="reviewer"
                meta="finished · read-only"
              />
            </li>
          </ul>
        </div>
      </ThemeFlip>
    </section>
  );
}
