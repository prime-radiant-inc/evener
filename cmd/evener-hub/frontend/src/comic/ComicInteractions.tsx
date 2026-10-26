import { AskDock, AskDockAnnouncements } from "../panes/session/composer/askDock/AskDock";
import { SandboxEscalationRail } from "../panes/session/transcript/tools/sandboxEscalation";
import styles from "./comic.module.css";

/** Selected-page controls read the foreground model, never the comic projection.
 * Runtime admission remains authoritative: inline child readers never mount this.
 */
export function ComicInteractions({ sessionRef }: { sessionRef: string }): React.ReactElement {
  return (
    <section className={styles.interactions} aria-label="Pending questions and approvals">
      <AskDockAnnouncements ref={sessionRef} />
      <AskDock key={`ask:${sessionRef}`} ref={sessionRef} />
      {/* Local busy/error state belongs to this ref and its request IDs. A late
          RPC settles through the shared owner, not a replacement rail. */}
      <SandboxEscalationRail key={sessionRef} sessionRef={sessionRef} />
    </section>
  );
}
