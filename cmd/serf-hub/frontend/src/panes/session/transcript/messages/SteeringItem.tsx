// The steering item renderer. Source-discriminated per parity issue #24
// (internal/appprojector/appwire_projection.go:588's own comment,
// internal/apptranscript/apptranscript.go:225-228): steering the human
// typed themselves (item.source === "user") is indistinguishable from a
// normal prompt and reuses UserMessageView verbatim; daemon-originated
// steering (no source, or anything else) renders as a quiet, collapsed-by-
// default divider instead - the reader never authored it, so it stays out
// of the way unless opened.

import { requireClass } from "../../../../widgets/internal/requireClass";
import { type ItemRenderProps, registerItemRenderer } from "../types";
import styles from "./steeringitem.module.css";
import { UserMessageView } from "./UserMessageItem";

const CLASS = {
  details: requireClass(styles.details, "steeringitem.module.css", "details"),
  summary: requireClass(styles.summary, "steeringitem.module.css", "summary"),
  body: requireClass(styles.body, "steeringitem.module.css", "body"),
};

export function SteeringItem({ item }: ItemRenderProps) {
  if (item.source === "user") return <UserMessageView item={item} />;
  if (!item.text) return null; // no text, no images path here (daemon steering never gets thumbnails - see below) - nothing to show
  return (
    <details className={CLASS.details} data-testid="steering-item">
      <summary className={CLASS.summary}>Steering injected</summary>
      {/* Daemon-sourced steering images are never rendered as thumbnails -
       * only ever as a placeholder baked into item.text server-side
       * (internal/apptranscript/apptranscript.go's ImagePlaceholder call) -
       * so, unlike UserMessageView, there is no images branch here at all. */}
      <pre className={CLASS.body}>{item.text}</pre>
    </details>
  );
}

registerItemRenderer("steering", SteeringItem);
