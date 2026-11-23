// The transcript's one speaker header, rendered by both message items.
import type { JSX, ReactNode } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./speakerheader.module.css";

const CLASS = {
  header: requireClass(styles.header, "speakerheader.module.css", "header"),
  name: requireClass(styles.name, "speakerheader.module.css", "name"),
  meta: requireClass(styles.meta, "speakerheader.module.css", "meta"),
};

export function SpeakerHeader({
  name,
  meta,
  actions,
  className,
  testId,
}: {
  name: string;
  meta?: ReactNode;
  actions?: ReactNode;
  className?: string;
  testId?: string;
}): JSX.Element {
  return (
    <div className={className ? `${CLASS.header} ${className}` : CLASS.header} data-testid={testId}>
      <span className={CLASS.name}>{name}</span>
      {meta !== undefined && <span className={CLASS.meta}>{meta}</span>}
      {actions}
    </div>
  );
}
