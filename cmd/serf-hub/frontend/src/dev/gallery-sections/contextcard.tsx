import { ContextCard } from "../../widgets/contextcard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./contextcard.module.css";

export default function ContextCardGallerySection() {
  return (
    <section>
      <h2>ContextCard</h2>
      <ThemeFlip>
        <div className={styles.grid}>
          <ContextCard
            source="docs/superpowers/specs/2026-08-13-webui-beautiful-ui-retheme-design.md"
            snippet="Neutral grays replace Fjord (cool blue) and Ledger (warm paper). Dark stays the default. The token-contract enforcement machinery is retained; the visual language is replaced."
            meta="1.2k chars"
            href="https://example.internal/docs/retheme-design"
          />
          <ContextCard
            source="src/widgets/card/index.tsx"
            snippet="A raised surface for grouping related content — its 1px ring comes from --shadow-card, not a border."
            meta="640 chars"
          />
          <ContextCard
            source="https://pkg.go.dev/context"
            snippet="Package context defines the Context type, which carries deadlines, cancellation signals, and other request-scoped values across API boundaries and between processes."
            href="https://pkg.go.dev/context"
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
