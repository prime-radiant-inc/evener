import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { RadioGroup, type RadioGroupOption, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { Code } from "./settingsField";
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

// "Collapsed" is the display label for prefs.ts's own "rail" value - matches
// the legacy's own theme.html radio (value="rail", visible text "Collapsed").
const SIDEBAR_MODE_OPTIONS: RadioGroupOption[] = [
  { value: "auto", label: "Auto" },
  { value: "pane", label: "Pane" },
  { value: "rail", label: "Collapsed" },
];

const FONT_SIZE_OPTIONS: RadioGroupOption[] = [
  { value: "s", label: "S" },
  { value: "m", label: "M" },
  { value: "l", label: "L" },
  { value: "xl", label: "XL" },
];

/**
 * Settings -> Theme (parity-m7-settings.md §3): 4 localStorage-only
 * preferences, no wire access - every control here reads/writes prefs.ts
 * directly. Unlike the legacy's own htmx world (server-rendered radios with
 * no `checked` attribute, resynced by a dedicated applyAppearanceState()
 * rerun on every load/swap - see that section's own floor-doc bullet), a
 * RadioGroup driven straight off usePrefsStore is always correct on every
 * render; there is no separate "reapply" step to port.
 *
 * Only Color theme toasts on change ("Theme: {value}", matching the
 * legacy's own asymmetry: phone density/sidebar mode/font size get no
 * toast at all in assets/settings-appearance.js either).
 *
 * Known copy gap (not this task's to resolve): "default follows your OS
 * preference" carries over the legacy's exact copy, but src/styles/
 * tokens.css has no prefers-color-scheme media query - "system" always
 * renders the dark tokens today (see prefs.ts's own top comment). Flagged
 * in the wave-7 report rather than silently rewritten.
 */
export function ThemeSection() {
  const theme = usePrefsStore((s) => s.theme);
  const phoneDensity = usePrefsStore((s) => s.phoneDensity);
  const sidebarMode = usePrefsStore((s) => s.sidebarMode);
  const fontSize = usePrefsStore((s) => s.fontSize);
  const { push } = useToasts();

  return (
    <div className={CLASS.root}>
      <p className={CLASS.intro}>Theme, density, and sidebar mode are saved per-browser.</p>

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
        <p className={CLASS.help}>Type-scale variant on phone (≤767px). Compact is the default.</p>
      </div>

      <div className={CLASS.row}>
        <RadioGroup
          label="Sidebar mode"
          value={sidebarMode}
          options={SIDEBAR_MODE_OPTIONS}
          onChange={(value) => prefsStore.getState().setSidebarMode(value as "auto" | "pane" | "rail")}
        />
        <p className={CLASS.help}>
          Desktop only. Collapsed hides the sidebar entirely — reopen it with the ☰ chip (top-left) as an overlay
          drawer; Auto collapses below 1200px and expands above it. <Code>⌘B</Code> cycles collapsed → pane → auto.
        </p>
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
    </div>
  );
}
