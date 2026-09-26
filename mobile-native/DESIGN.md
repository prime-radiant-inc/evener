---
name: Evener Native Project Browser
description: Project first session browsing for the native mobile Sessions surface.
colors:
  light-background: "#FAF9F6"
  light-surface: "#F4F3EE"
  light-text: "#252521"
  light-secondary: "#5F5F57"
  light-border: "#DDDCD4"
  light-accent: "#0064C2"
  light-accent-fill: "#0070E0"
  light-error: "#C51D23"
  dark-background: "#191918"
  dark-surface: "#20201E"
  dark-text: "#F2F1EB"
  dark-secondary: "#B0AFA6"
  dark-border: "#34342F"
  dark-accent: "#459EFF"
  dark-accent-fill: "#0070E0"
  dark-error: "#F17478"
typography:
  project-header:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "19px"
    fontWeight: 600
  session-title:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "17px"
    lineHeight: "23px"
  body:
    fontFamily: "Source Serif 4, Georgia, serif"
    fontSize: "17px"
    lineHeight: "26px"
  metadata:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "13px"
    lineHeight: "19px"
rounded:
  input: "9px"
  action: "24px"
spacing:
  content-inset: "20px"
  nested-inset: "40px"
  action-target: "44px"
  session-min-height: "68px"
components:
  project-header:
    textColor: "{colors.light-text}"
    typography: "{typography.project-header}"
    height: "52px"
  session-row:
    textColor: "{colors.light-text}"
    typography: "{typography.session-title}"
    height: "{spacing.session-min-height}"
  search-field:
    backgroundColor: "{colors.light-surface}"
    textColor: "{colors.light-text}"
    rounded: "{rounded.input}"
    height: "48px"
---

# Design System: Evener Native Project Browser

## Overview

**Creative North Star: "The project first work surface"**

This records the accepted native Sessions browser at source `f75411ead`. Projects provide orientation, sessions remain the work items, and conversation content stays the visual center. The browser uses native navigation, SF system typography, semantic light and dark surfaces, and touch sized controls. The underlying mobile UX philosophy, style guide and dated project-browser review remain in the preserved development branch `live-concepts-plan2-integrate` at checkpoint `04ae937af`.

The main Sessions surface is one expandable scrolling hierarchy. A dedicated Project route is a separate detail surface and may use its own current, recent and archived tabs. These are distinct navigation contracts.

**Key Characteristics:**
- Project headers first; nested session rows remain in the same scroll surface.
- Quiet idle rows, with state text reserved for active work and decisions.
- Native actions, guarded paging, retained browser state, and explicit recovery.

## Colors

The implementation switches between light and dark palettes through the native color scheme. The values live in `src/design/tokens.ts`, which follows the redesign spec (`docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 16.1); change them there first.

### Primary
- **Accent** (`#0064C2` light, `#459EFF` dark): Search, links and selected controls. Primary buttons fill with accent-fill (`#0070E0` in both themes) and carry white text.

### Neutral
- **Paper background** (`#FAF9F6` light, `#191918` dark): Main reading and browsing canvas.
- **Soft surface** (`#F4F3EE` light, `#20201E` dark): Inputs and raised native-looking controls.
- **Primary text** (`#252521` light, `#F2F1EB` dark): Project and session content.
- **Secondary text** (`#5F5F57` light, `#B0AFA6` dark): Counts, metadata, and quiet state explanations.
- **Quiet border** (`#DDDCD4` light, `#34342F` dark): Sparse row and input boundaries.
- **Semantic error** (`#C51D23` light, `#F17478` dark): Read and action failures.

### Named Rules
**The Quiet Idle Rule.** Omit idle status noise from session rows; reserve state text for working, questions, warnings, and failures.

## Typography

Dimensions below describe React Native logical units (points on iOS). The `px` values in the serialized tokens support documentation previews; they are not physical screen pixels or CSS used by the app.

**Display Font:** SF system (with the platform system fallback)
**Body Font:** Source Serif 4 for conversation prose (agent prose 17/26, your messages 17/25); headings inside a reply are SF Pro semibold at 20/17/15, and code is Menlo. See `typeRoles` in `src/design/tokens.ts`, which follows the redesign spec (`docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 16.2).

**Character:** A compact native hierarchy gives project names clear priority while leaving session prose comfortable to read and metadata subordinate.

### Hierarchy
- **Project header** (600, 19px): Project name, up to two lines.
- **Session title** (400, 17px, 23px line-height): Session row title, up to two lines.
- **Body** (Source Serif 4, 17px, 26px line-height for agent prose / 25px for your messages): Conversation and readable content.
- **Metadata** (400, 13px, 19px line-height): Counts, paths, and subordinate state.

### Named Rules
**The Two Level Rule.** Cap nested session indentation at two levels so the title retains reading width.

## Layout

Use one native scrolling list for the main Sessions browser. Project headers begin after a 20 logical-unit horizontal inset, nested content begins at 40 logical units plus at most two 12-unit depth increments, and session rows reserve at least 68 logical units. The first project opens and loads on initial focus; other projects load on demand. Search can occupy its own row at larger text sizes. Boundary loading indicators remain in the list; retain loaded rows when returning from a conversation.

## Elevation & Depth

The browser uses tonal layering and sparse borders rather than shadows, gradients, bevels, or decorative cards. Inputs use the surface tone and a 1-unit border; project and session groups are defined by spacing and sparse separators.

### Named Rules
**The Flat Browser Rule.** Use background, surface, spacing, and sparse borders to convey structure; do not add decorative elevation to ordinary rows.

## Shapes

Ordinary transcript and list content stays open on the reading surface. The search field has a gently rounded 9-unit corner. Primary action pills use the native action treatment with a 24-unit radius. Every action preserves a native minimum target of 44 units on iOS.

## Components

### Project headers
- **Shape:** Open row with a sparse bottom boundary; no enclosing card.
- **Typography:** 19 pt semibold title and subdued session count.
- **Behavior:** Chevron toggles the nested browser; More/details is a separate 44-unit affordance that opens the dedicated Project route.

### Session rows
- **Shape:** Open rows, 68-unit minimum height, 40-unit nested inset plus capped depth.
- **Typography:** 17 pt title with 23 pt line-height; metadata is 13 pt with 19 pt line-height.
- **Behavior:** Tapping the row opens a conversation. Related-session disclosure is separate from opening.

### Search field
- **Style:** 48-unit minimum height, 9-unit radius, soft surface fill, quiet border.
- **Behavior:** Keep the query visible while loading and returning; use visible Search and Clear actions, with the full accessible label `Search sessions`.

### Loading and recovery
- **Style:** Quiet activity indicator or muted explanatory copy outside row content.
- **Behavior:** Initial project loading, automatic boundary paging, stale snapshots, and read errors each preserve a reachable retry or refresh path. Keep controls mounted and disable them while unavailable.

### Message actions
- **Style:** A three-dot 44-unit action beside eligible user messages.
- **Behavior:** iOS uses the native `ActionSheetIOS` action surface with the selected message title, Fork from here, and Cancel. The Fork preview is read-only; explanatory copy says the created fork opens with an editable draft.

## Do's and Don'ts

### Do:
- **Do** lead the main Sessions surface with projects and expand sessions in place.
- **Do** load the first project initially and lazy-load other project groups.
- **Do** preserve browser and conversation reading state across navigation.
- **Do** keep capability affordances mounted and disable them during unavailable or refreshing states.
- **Do** use human readable state copy and explicit stale/error recovery.

### Don't:
- **Don't** describe the main expandable browser as the dedicated Project route's tabbed detail view.
- **Don't** wrap every ordinary row in a rounded card or add decorative shadows.
- **Don't** imply that local warmed transport measurements establish phone or real-network performance.
- **Don't** make status, counts, paths, or internal IDs compete with the session title.
