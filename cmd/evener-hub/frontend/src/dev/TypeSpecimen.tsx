import { requireClass } from "../widgets/internal/requireClass";
import { ThemeFlip } from "./ThemeFlip";
import styles from "./typespecimen.module.css";

const MODULE = "typespecimen.module.css";
const CLASS = {
  page: requireClass(styles.page, MODULE, "page"),
  intro: requireClass(styles.intro, MODULE, "intro"),
  section: requireClass(styles.section, MODULE, "section"),
  sectionTitle: requireClass(styles.sectionTitle, MODULE, "sectionTitle"),
  note: requireClass(styles.note, MODULE, "note"),
  rowLabel: requireClass(styles.rowLabel, MODULE, "rowLabel"),
  rampRow: requireClass(styles.rampRow, MODULE, "rampRow"),
  rampSample: requireClass(styles.rampSample, MODULE, "rampSample"),
  sizeCaption: requireClass(styles.sizeCaption, MODULE, "sizeCaption"),
  sizeUi: requireClass(styles.sizeUi, MODULE, "sizeUi"),
  sizeBody: requireClass(styles.sizeBody, MODULE, "sizeBody"),
  sizePaneTitle: requireClass(styles.sizePaneTitle, MODULE, "sizePaneTitle"),
  sizePageTitle: requireClass(styles.sizePageTitle, MODULE, "sizePageTitle"),
  sizeDisplay: requireClass(styles.sizeDisplay, MODULE, "sizeDisplay"),
  leadingRow: requireClass(styles.leadingRow, MODULE, "leadingRow"),
  leadingSample: requireClass(styles.leadingSample, MODULE, "leadingSample"),
  leadingUi: requireClass(styles.leadingUi, MODULE, "leadingUi"),
  leadingBody: requireClass(styles.leadingBody, MODULE, "leadingBody"),
  leadingTitle: requireClass(styles.leadingTitle, MODULE, "leadingTitle"),
  eyebrow: requireClass(styles.eyebrow, MODULE, "eyebrow"),
  rhythmRow: requireClass(styles.rhythmRow, MODULE, "rhythmRow"),
  rhythmBar: requireClass(styles.rhythmBar, MODULE, "rhythmBar"),
  barLine: requireClass(styles.barLine, MODULE, "barLine"),
  barItem: requireClass(styles.barItem, MODULE, "barItem"),
  barGroup: requireClass(styles.barGroup, MODULE, "barGroup"),
  barExchange: requireClass(styles.barExchange, MODULE, "barExchange"),
  measures: requireClass(styles.measures, MODULE, "measures"),
  measureColumn: requireClass(styles.measureColumn, MODULE, "measureColumn"),
  measureLabel: requireClass(styles.measureLabel, MODULE, "measureLabel"),
  measureValue: requireClass(styles.measureValue, MODULE, "measureValue"),
  prose: requireClass(styles.prose, MODULE, "prose"),
  measureReading: requireClass(styles.measureReading, MODULE, "measureReading"),
  measureWide: requireClass(styles.measureWide, MODULE, "measureWide"),
};

const RAMP_SAMPLE = "Sphinx of black quartz, judge my vow — 0123456789";

// The label is <token suffix> <base px>: the size a step renders at with
// the font-size preference on M. S/L/XL scale the whole ramp through
// --font-scale, so the ratios between these rows never change.
const RAMP: { label: string; sizeClass: string }[] = [
  { label: "caption 12", sizeClass: CLASS.sizeCaption },
  { label: "ui 13", sizeClass: CLASS.sizeUi },
  { label: "body 15", sizeClass: CLASS.sizeBody },
  { label: "pane-title 18", sizeClass: CLASS.sizePaneTitle },
  { label: "page-title 22", sizeClass: CLASS.sizePageTitle },
  { label: "display 28", sizeClass: CLASS.sizeDisplay },
];

const LEADING_SAMPLE =
  "Three lines of one sentence, set at one size, so the only thing that changes between these samples is the leading itself.";

const LEADING: { label: string; leadingClass: string }[] = [
  { label: "line-height-ui", leadingClass: CLASS.leadingUi },
  { label: "line-height-body", leadingClass: CLASS.leadingBody },
  { label: "line-height-title", leadingClass: CLASS.leadingTitle },
];

// The label is <token suffix> <resolved px>: each --rhythm-* step aliases
// a --space-* value, and the bar beside it is exactly that tall.
const RHYTHM: { label: string; barClass: string }[] = [
  { label: "rhythm-line 4", barClass: CLASS.barLine },
  { label: "rhythm-item 8", barClass: CLASS.barItem },
  { label: "rhythm-group 16", barClass: CLASS.barGroup },
  { label: "rhythm-exchange 24", barClass: CLASS.barExchange },
];

// 600 characters of ordinary prose - long enough that the two measures
// differ by more than one line, short enough to read both before deciding.
const MEASURE_PROSE =
  "A session is a conversation with an agent that can read your files, run commands, and delegate work to other agents. The transcript is the record of that conversation: what you asked, what the agent decided, which tools it reached for, and what came back. Most of it is prose, and prose is what a measure is for. A column this wide holds roughly ninety characters to the line, close to the range typographers argued for long before screens, and it still leaves room for a hundred-column code block without a horizontal scrollbar. Read a few lines of each column and pick whichever one tires you less.";

const MEASURES: { name: string; value: string; widthClass: string }[] = [
  { name: "Reading measure", value: "44rem", widthClass: CLASS.measureReading },
  { name: "Wide measure", value: "64rem", widthClass: CLASS.measureWide },
];

/**
 * The specimen body, rendered once per theme by ThemeFlip. Everything here
 * is set from a token and nothing else - the page's whole job is to show
 * what the ramp, the leading, the eyebrow recipe, the rhythm steps and the
 * two measures actually look like, so a change to any of them is reviewed
 * as a picture instead of a diff (critique R1-R3, R10).
 */
function Specimen() {
  return (
    <>
      <section className={CLASS.section}>
        <h2 className={CLASS.sectionTitle}>Ramp</h2>
        <p className={CLASS.note}>
          Each row is set in its own --font-size-* token. The number is the base px at font-size M.
        </p>
        {RAMP.map(({ label, sizeClass }) => (
          <div className={CLASS.rampRow} key={label}>
            <p className={CLASS.rowLabel}>{label}</p>
            <p className={`${CLASS.rampSample} ${sizeClass}`}>{RAMP_SAMPLE}</p>
          </div>
        ))}
      </section>

      <section className={CLASS.section}>
        <h2 className={CLASS.sectionTitle}>Leading</h2>
        <p className={CLASS.note}>Same size, same column width; only line-height differs.</p>
        {LEADING.map(({ label, leadingClass }) => (
          <div className={CLASS.leadingRow} key={label}>
            <p className={CLASS.rowLabel}>{label}</p>
            <p className={`${CLASS.leadingSample} ${leadingClass}`}>{LEADING_SAMPLE}</p>
          </div>
        ))}
      </section>

      <section className={CLASS.section}>
        <h2 className={CLASS.sectionTitle}>Eyebrow</h2>
        <p className={CLASS.eyebrow}>Recommended</p>
        <p className={CLASS.note}>
          The one uppercase recipe: --font-size-caption, --font-weight-medium, --ink-mid, uppercase, letter-spacing
          --tracking-eyebrow. Two words at most; never a sentence.
        </p>
      </section>

      <section className={CLASS.section}>
        <h2 className={CLASS.sectionTitle}>Rhythm</h2>
        <p className={CLASS.note}>
          Each bar is as tall as the step it names — the four gaps a surface may put between things.
        </p>
        {RHYTHM.map(({ label, barClass }) => (
          <div className={CLASS.rhythmRow} key={label}>
            <p className={CLASS.rowLabel}>{label}</p>
            <div className={`${CLASS.rhythmBar} ${barClass}`} />
          </div>
        ))}
      </section>

      <section className={CLASS.section}>
        <h2 className={CLASS.sectionTitle}>Measure</h2>
        <p className={CLASS.note}>
          The same 600-character paragraph at body size and --line-height-body, at both measures.
        </p>
        <div className={CLASS.measures}>
          {MEASURES.map(({ name, value, widthClass }) => (
            <div className={CLASS.measureColumn} key={value}>
              <p className={CLASS.measureLabel}>
                {name} <span className={CLASS.measureValue}>{value}</span>
              </p>
              <p className={`${CLASS.prose} ${widthClass}`}>{MEASURE_PROSE}</p>
            </div>
          ))}
        </div>
      </section>
    </>
  );
}

/**
 * `/dev/type`: the type specimen. Dev build only — see src/App.tsx, which
 * gates the route (and this module's import) behind import.meta.env.DEV so
 * it never reaches a production bundle, exactly as it does for
 * /dev/widgets and /dev/surfaces.
 */
export default function TypeSpecimen() {
  return (
    <div className={CLASS.page}>
      <p className={CLASS.intro}>
        Type specimen — the ramp, the leading, the eyebrow recipe, the rhythm steps and the two measures, rendered from
        the real tokens in both themes.
      </p>
      <ThemeFlip>
        <Specimen />
      </ThemeFlip>
    </div>
  );
}
