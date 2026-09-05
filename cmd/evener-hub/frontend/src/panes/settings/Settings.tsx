import type { ComponentType } from "react";
import { useEffect } from "react";
import { chromeStore } from "../../shell/chromeStore";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { useIsMobile } from "../../shell/useIsMobile";
import { workspaceStore } from "../../shell/workspace";
import { useSettingsOverviewStore } from "../../stores/settingsOverview";
import { IconButton, PaneScaffold } from "../../widgets";
import { CloseIcon } from "../../widgets/dialog/CloseIcon";
import { requireClass } from "../../widgets/internal/requireClass";
import { SettingsNav } from "./SettingsNav";
import { DEFAULT_SECTION_ID, settingsSectionLabel } from "./sections";
import { AboutSection } from "./sections/about";
import { AgentsSection } from "./sections/agents";
import { CredentialsSection } from "./sections/credentials/CredentialsSection";
import { DisplaySection } from "./sections/display";
import { GeneralSection } from "./sections/general";
import { HubSection } from "./sections/hub";
import { InRepoSection } from "./sections/inrepo";
import { LaunchServerSection } from "./sections/launchServer";
import { MarketplacesPluginsSection } from "./sections/marketplacesPlugins";
import { McpSection } from "./sections/mcp";
import { MobileSection } from "./sections/mobile";
import { NotificationsSection } from "./sections/notifications";
import { PlaceholderSection } from "./sections/PlaceholderSection";
import { PluginsDirsSection } from "./sections/pluginsDirs";
import { ProjectSection } from "./sections/project";
import { SkillsDirsSection } from "./sections/skillsDirs";
import { StorageSection } from "./sections/storage";
import { ThemeSection } from "./sections/theme";
import { TranscriptSection } from "./sections/transcript";
import styles from "./settings.module.css";

// McpSection's overview dependency is injected (its own McpSectionProps seam,
// built against the pinned interface before the real store existed) - this
// adapter binds it to the real store at the one place the dispatch map needs
// a zero-prop component.
function McpSectionWired() {
  return <McpSection useOverviewStore={useSettingsOverviewStore} />;
}

export interface SettingsPaneParams {
  section?: string;
}

const CLASS = {
  shell: requireClass(styles.shell, "settings.module.css", "shell"),
  shellDetail: requireClass(styles.shellDetail, "settings.module.css", "shellDetail"),
  content: requireClass(styles.content, "settings.module.css", "content"),
};

// SECTION_COMPONENTS: the per-section dispatch seam T1b's own placeholder
// wiring deliberately left unbuilt ("a lookup mechanism this task
// deliberately doesn't guess at - see the wave-7 report" - PlaceholderSection.
// tsx's own doc comment). Controller-merged, single-line-append discipline,
// same as the pane registry/route table/widgets barrel (wave-7 plan's own
// "Shared collision surfaces" note) - each of T2/T3/T4 adds its own import
// line(s) plus map entries here; a section id with no entry falls back to
// PlaceholderSection. "project" is deliberately absent from
// SETTINGS_SECTIONS (sections.ts's own comment - no nav entry) but IS a
// valid dispatch target here, reached via /settings/project?cwd=.
const SECTION_COMPONENTS: Record<string, ComponentType<{ sectionId: string }>> = {
  credentials: CredentialsSection,
  agents: AgentsSection,
  "launch-evener": LaunchServerSection,
  inrepo: InRepoSection,
  project: ProjectSection,
  "plugins-manager": MarketplacesPluginsSection,
  plugins: PluginsDirsSection,
  skills: SkillsDirsSection,
  mcp: McpSectionWired,
  general: GeneralSection,
  theme: ThemeSection,
  transcript: TranscriptSection,
  display: DisplaySection,
  notifications: NotificationsSection,
  hub: HubSection,
  mobile: MobileSection,
  storage: StorageSection,
  about: AboutSection,
};

// The "up" target a focused section publishes to the chrome store's paneBack
// channel - the section list, i.e. bare /settings. Module-level because it
// closes over nothing: publishing the SAME function identity across param
// changes keeps StackHost's subscription from re-rendering on every section
// switch.
function showSettingsList(): void {
  const url = paneToURL("settings", {});
  if (url !== null) navigate(url);
}

/**
 * The settings pane shell: left nav + section content on desktop; on mobile
 * (<900px, useIsMobile) a URL-derived master-detail pair (2026-08-16
 * settings mobile-nav design).
 *
 * The URL owns the mobile view, not component state: bare /settings (no
 * section param) IS the section list - the drill-down's root - and
 * /settings/{section} is that section's detail. Reload, share, deep link,
 * and the browser's own back/forward all land on an honest view because
 * each level is addressable (routing.ts already resolves both forms;
 * AppShell's replacePrimary glue updates this singleton pane's params in
 * place on every popstate). Desktop is unchanged: nav and content sit side
 * by side, and a bare /settings still resolves its content to
 * DEFAULT_SECTION_ID.
 *
 * Back on mobile lives in the shell's top bar, not in the content: a
 * focused section publishes showSettingsList to the chrome store's paneBack
 * channel (the workspace can't see section switches - a singleton pane's id
 * never changes - so StackHost's pane-stack back could never walk them;
 * chromeStore.ts's own comment documents the channel). The list publishes
 * nothing: it IS the root, so the top-bar Back there keeps its ordinary
 * meaning (exit settings). Host-agnostic, like the title channel - DockHost
 * never reads paneBack.
 *
 * The nav is NEVER unmounted, in either host or view: while a mobile detail
 * shows, the .shellDetail marker class CSS-hides it (a compound selector -
 * `.shellDetail .nav` beats `.nav`'s own display:flex on specificity, the
 * trap the legacy's `hidden`-attribute toggling fell into), so its filter
 * text and scroll position survive the drill-down round trip.
 */
export default function Settings({ params, paneId }: PaneProps<SettingsPaneParams>) {
  const activeId = params.section ?? DEFAULT_SECTION_ID;
  const isMobile = useIsMobile();
  const SectionComponent = SECTION_COMPONENTS[activeId] ?? PlaceholderSection;
  // Mobile list view: bare /settings on a phone. No row is "active" - there
  // is no content beside the list for a highlight to refer to.
  const showingList = isMobile && params.section === undefined;
  const showContent = !isMobile || params.section !== undefined;

  function handleNavigate(sectionId: string) {
    const url = paneToURL("settings", { section: sectionId });
    if (url !== null) navigate(url);
  }

  // paneBack is published whenever a section is focused, in EITHER host -
  // host-agnostic like the title channel. The effect (not render) owns the
  // store write so unmount always clears it, never leaving a stale handler
  // for the next pane StackHost mounts.
  useEffect(() => {
    if (params.section === undefined) {
      chromeStore.getState().setPaneBack(null);
      return undefined;
    }
    chromeStore.getState().setPaneBack(showSettingsList);
    return () => chromeStore.getState().setPaneBack(null);
  }, [params.section]);

  // Settings has no other way out: opened via the rail gear, it took over
  // the main slot (workspace.ts's own "primary" rule) whose tab bar/close
  // affordance DockHost deliberately suppresses (DockHost.test.tsx: "the
  // main pane offers no way to close it - it is replaceable, not
  // closeable"). closePane on a main pane is exactly that replace - the
  // main slot is never left empty, so DockHost relaunches welcome there -
  // which makes it the correct exit here too, just reached from inside the
  // pane instead of a tab (x) that main panes intentionally don't have.
  //
  // navigate() to "/" FIRST, same seam needsYouCycle.ts documents for the
  // identical trap: closePane alone changes the workspace store but leaves
  // window.location.pathname on /settings/*, and AppShell's own
  // route-reconciliation effect reconciles the CURRENT pathname against the
  // workspace on every pane change - so a pane change with no matching URL
  // change gets undone, reinstating settings right back into main (observed
  // in a real browser: Escape/close did nothing). Routing through
  // paneToURL/navigate keeps the URL and the workspace in agreement, so
  // there is nothing left for reconciliation to "fix".
  function handleClose() {
    const url = paneToURL("welcome", {});
    if (url !== null) navigate(url);
    workspaceStore.getState().closePane(paneId);
  }

  // Escape closes the pane. A React onKeyDown on the pane's own div only
  // fires when focus is INSIDE it — and the common open path (clicking the
  // rail's gear) leaves focus on the gear button, so in a real browser the
  // pane-scoped handler never saw the key (live-verified regression). A
  // document-level bubble-phase listener sees Escape regardless of where
  // focus sits, and still yields to anything inside settings that claimed
  // the key first: a Dialog/Menu preventDefaults its own Escape (see
  // OverlayPanel/Menu), which this checks before closing.
  useEffect(() => {
    function onDocumentKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key !== "Escape") return;
      if (event.defaultPrevented) return;
      handleClose();
    }
    document.addEventListener("keydown", onDocumentKeyDown);
    return () => document.removeEventListener("keydown", onDocumentKeyDown);
  });

  return (
    <PaneScaffold
      title={settingsSectionLabel(activeId)}
      mobileTitle={showingList ? "Settings" : undefined}
      actions={
        <IconButton label="Close settings" icon={<CloseIcon />} variant="quiet" size="sm" onClick={handleClose} />
      }
    >
      <div className={params.section !== undefined ? `${CLASS.shell} ${CLASS.shellDetail}` : CLASS.shell}>
        <SettingsNav activeId={showingList ? null : activeId} onNavigate={handleNavigate} />
        {showContent && (
          <div className={CLASS.content} data-testid="settings-content">
            <SectionComponent sectionId={activeId} />
          </div>
        )}
      </div>
    </PaneScaffold>
  );
}
