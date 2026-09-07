import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { RadioGroup, type RadioGroupOption, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./theme.module.css";

const CLASS = {
  root: requireClass(styles.root, "theme.module.css", "root"),
  intro: requireClass(styles.intro, "theme.module.css", "intro"),
  row: requireClass(styles.row, "theme.module.css", "row"),
  help: requireClass(styles.help, "theme.module.css", "help"),
};

const THEME_OPTIONS: RadioGroupOption[] = [
  { value: "system", label: "System" },
  { value: "dark", label: "Dark" },
  { value: "light", label: "Light" },
];

const PHONE_DENSITY_OPTIONS: RadioGroupOption[] = [
  { value: "compact", label: "Compact" },
  { value: "comfortable", label: "Comfortable" },
];

const FONT_SIZE_OPTIONS: RadioGroupOption[] = [
  { value: "s", label: "S" },
  { value: "m", label: "M" },
  { value: "l", label: "L" },
  { value: "xl", label: "XL" },
];

const TRANSCRIPT_MEASURE_OPTIONS: RadioGroupOption[] = [
  { value: "reading", label: "Reading" },
  { value: "wide", label: "Wide" },
];

/**
 * Settings -> Theme (parity-m7-settings.md §3): 3 localStorage-only
 * preferences, no wire access - every control here reads/writes prefs.ts
 * directly. A RadioGroup driven straight off usePrefsStore is always correct
 * on every render; there is no separate "reapply" step. (The former Sidebar
 * mode control was removed 2026-07-24 with collapsed mode itself — the
 * sidebar is always docked on desktop.)
 *
 * Only Color theme toasts on change ("Theme: {value}", matching the
 * legacy's own asymmetry: phone density/font size get no
 * toast at all in assets/settings-appearance.js either).
 *
 * "Both palettes ship; default follows your OS preference" (the Color
 * theme help copy below) is a real, live guarantee, not just carried-over
 * legacy copy: prefs.ts's own setTheme/applyTheme resolve "system" against
 * prefers-color-scheme and keep tracking it for as long as the tab stays
 * on "system" - see that file's own top comment for the mechanism (W7
 * close-fix round; before this, "system" always rendered dark regardless
 * of the OS, which this exact copy used to overclaim).
 */
export function ThemeSection() {
  const theme = usePrefsStore((s) => s.theme);
  const phoneDensity = usePrefsStore((s) => s.phoneDensity);
  const fontSize = usePrefsStore((s) => s.fontSize);
  const transcriptMeasure = usePrefsStore((s) => s.transcriptMeasure);
  const { push } = useToasts();

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Theme, density, font size and transcript width are saved per-browser.</p>

      <div className={CLASS.row}>
        <RadioGroup
          label="Color theme"
          value={theme}
          options={THEME_OPTIONS}
          onChange={(value) => {
            prefsStore.getState().setTheme(value as "system" | "dark" | "light");
            push("success", `Theme: ${value}`);
          }}
        />
        <p className={CLASS.help}>Both palettes ship; default follows your OS preference.</p>
      </div>

      <div className={CLASS.row}>
        <RadioGroup
          label="Phone density"
          value={phoneDensity}
          options={PHONE_DENSITY_OPTIONS}
          onChange={(value) => prefsStore.getState().setPhoneDensity(value as "compact" | "comfortable")}
        />
        <p className={CLASS.help}>Scales line spacing on phones (≤900px). Compact is the default.</p>
      </div>

      <div className={CLASS.row}>
        <RadioGroup
          label="Font size"
          value={fontSize}
          options={FONT_SIZE_OPTIONS}
          onChange={(value) => prefsStore.getState().setFontSize(value as "s" | "m" | "l" | "xl")}
        />
        <p className={CLASS.help}>Scales all UI text. M is the default.</p>
      </div>

      <div className={CLASS.row}>
        <RadioGroup
          label="Transcript width"
          value={transcriptMeasure}
          options={TRANSCRIPT_MEASURE_OPTIONS}
          onChange={(value) => prefsStore.getState().setTranscriptMeasure(value as "reading" | "wide")}
        />
        <p className={CLASS.help}>Reading keeps lines near 90 characters. Wide uses more of the window.</p>
      </div>
    </div>
  );
}
