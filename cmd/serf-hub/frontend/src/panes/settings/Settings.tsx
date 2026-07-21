import { useState } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { useIsMobile } from "../../shell/useIsMobile";
import { PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { SettingsNav } from "./SettingsNav";
import { DEFAULT_SECTION_ID, settingsSectionLabel } from "./sections";
import { PlaceholderSection } from "./sections/PlaceholderSection";
import styles from "./settings.module.css";

export interface SettingsPaneParams {
  section?: string;
}

const CLASS = {
  shell: requireClass(styles.shell, "settings.module.css", "shell"),
  content: requireClass(styles.content, "settings.module.css", "content"),
  back: requireClass(styles.back, "settings.module.css", "back"),
};

/**
 * The settings pane shell: left nav + section content, mobile nav-as-page
 * (test-settings-shell.js) - below the shared shell's own <900px mobile
 * breakpoint (useIsMobile), the nav and the focused section's content are
 * two full-width views instead of a side-by-side split, toggled by a back
 * button rather than kept both mounted with CSS hiding (the legacy's own
 * `body[data-settings-pane]` needed explicit JS attribute toggling only
 * because HTML's native `hidden` attribute loses to a `display` override -
 * a CSS/HTML specificity conflict that plain React conditional rendering,
 * used here, cannot hit at all). A resolved section is always showing on
 * mount (bare /settings defaults to DEFAULT_SECTION_ID, mirroring the
 * legacy sidebar's own global Settings entry point landing on "general"),
 * so mobile's initial view is always the content view with its back
 * button visible - "nav" is reached only via that button's own click,
 * matching test-settings-shell.js's "back-button visible whenever an
 * Active section title is present" (always true here) and matching the
 * legacy's client-only URL reset on back (no refetch - the nav here was
 * never unmounted... except see below), respectively.
 *
 * The mobile nav view intentionally does NOT preserve SettingsNav's own
 * filter text across a round trip through the content view - it fully
 * unmounts (conditional rendering, not hidden) whenever the content view
 * is showing, so returning via Back always starts from a clear filter.
 * Desktop never unmounts it at all (both views always render), so filter
 * persistence there is automatic without any extra state here.
 */
export default function Settings({ params }: PaneProps<SettingsPaneParams>) {
  const activeId = params.section ?? DEFAULT_SECTION_ID;
  const isMobile = useIsMobile();
  const [mobileShowingNav, setMobileShowingNav] = useState(false);

  function handleNavigate(sectionId: string) {
    setMobileShowingNav(false);
    const url = paneToURL("settings", { section: sectionId });
    if (url !== null) navigate(url);
  }

  const showNav = !isMobile || mobileShowingNav;
  const showContent = !isMobile || !mobileShowingNav;

  return (
    <PaneScaffold title={settingsSectionLabel(activeId)}>
      <div className={CLASS.shell}>
        {showNav && <SettingsNav activeId={activeId} onNavigate={handleNavigate} />}
        {showContent && (
          <div className={CLASS.content}>
            {isMobile && (
              <button
                type="button"
                className={CLASS.back}
                aria-label="Back to settings"
                onClick={() => setMobileShowingNav(true)}
              >
                ‹ Settings
              </button>
            )}
            <PlaceholderSection sectionId={activeId} />
          </div>
        )}
      </div>
    </PaneScaffold>
  );
}
