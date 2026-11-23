// The shared meta row: aria-hidden separators between segments, optional trailing slot.
import { Fragment, type HTMLAttributes, type ReactNode } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./toolMeta.module.css";

const SEP = requireClass(styles.sep, "toolMeta.module.css", "sep");
const SPRING = requireClass(styles.spring, "toolMeta.module.css", "spring");

interface ToolMetaProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  segments: { key: string; content: ReactNode }[];
  trailing?: ReactNode;
}

export function ToolMeta({ segments, trailing, className, ...rest }: ToolMetaProps) {
  if (segments.length === 0 && trailing === undefined) return null;
  return (
    <div {...rest} className={className}>
      {segments.map((segment, index) => (
        <Fragment key={segment.key}>
          {index > 0 && (
            <span className={SEP} data-separator="" aria-hidden="true">
              ·
            </span>
          )}
          {segment.content}
        </Fragment>
      ))}
      {trailing !== undefined && (
        <>
          <span className={SPRING} />
          {trailing}
        </>
      )}
    </div>
  );
}
