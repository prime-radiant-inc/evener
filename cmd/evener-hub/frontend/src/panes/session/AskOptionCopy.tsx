import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./askoptioncopy.module.css";

const CLASS = {
  copy: requireClass(styles.optionCopy, "askoptioncopy.module.css", "optionCopy"),
  lead: requireClass(styles.optionLead, "askoptioncopy.module.css", "optionLead"),
  name: requireClass(styles.optionName, "askoptioncopy.module.css", "optionName"),
  recommended: requireClass(styles.optionRecommended, "askoptioncopy.module.css", "optionRecommended"),
  detail: requireClass(styles.optionDetail, "askoptioncopy.module.css", "optionDetail"),
};

interface AskOptionCopyProps {
  label: string;
  detail: string;
  recommended?: boolean;
}

/** The shared label, recommendation, and detail hierarchy for live choices and
 * read-only question history. The caller owns any input and selection row. */
export function AskOptionCopy({ label, detail, recommended = false }: AskOptionCopyProps) {
  return (
    <span className={CLASS.copy}>
      <span className={CLASS.lead}>
        <span className={CLASS.name}>{label}</span>
        {recommended && (
          <span className={CLASS.recommended} data-slot="recommended">
            · recommended
          </span>
        )}
      </span>
      {detail && <span className={CLASS.detail}>{detail}</span>}
    </span>
  );
}
