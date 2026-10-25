import type {
  NavigationProjectResource,
  NavigationProjectSummary,
  NavigationSessionSummary,
} from "@evener/appwire-client";
import {
  keyID,
  NAVIGATION_CATALOG_LIMIT,
  NAVIGATION_SECTION_LIMIT,
  selectDisplaySources,
  selectLiveRows,
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectProjectSummaries,
  selectSectionRemaining,
} from "@evener/appwire-client/state/navigation";
import { useState } from "react";
import { navigationStore, useNavigationStore } from "../stores/navigation/store";
import { Button } from "../widgets";

const catalogs = ["projects", "archived_projects"] as const;
type Catalog = (typeof catalogs)[number];
type Tier = "current" | "recent" | "archived";
const tiers: Tier[] = ["current", "recent", "archived"];

function SessionRows({ rows, onSelect }: { rows: readonly NavigationSessionSummary[]; onSelect(ref: string): void }) {
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  return (
    <ul>
      {rows.map((row) => (
        <li key={row.ref}>
          <Button variant="link" onClick={() => onSelect(row.ref)}>
            {row.title || row.ref}
          </Button>
          {row.children.length > 0 && (
            <Button
              variant="link"
              onClick={() =>
                setExpanded((old) => {
                  const next = new Set(old);
                  if (next.has(row.ref)) next.delete(row.ref);
                  else next.add(row.ref);
                  return next;
                })
              }
              aria-label={`Children of ${row.title || row.ref}`}
            >
              Children
            </Button>
          )}
          {expanded.has(row.ref) && row.children.length > 0 && <SessionRows rows={row.children} onSelect={onSelect} />}
        </li>
      ))}
    </ul>
  );
}

function Project({ project, onSelect }: { project: NavigationProjectSummary; onSelect(ref: string): void }) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState(false);
  const [pages, setPages] = useState<Partial<Record<Tier, number[]>>>({});
  const resource = useNavigationStore((s) => s.resources.get(keyID({ kind: "project", projectKey: project.key })));
  const pageResources = useNavigationStore((s) => s.resources);
  const load = () => {
    setOpen(true);
    void navigationStore
      .getState()
      .loadProject(project.key)
      .then(
        (result) => setError(!!result.error),
        () => setError(true),
      );
  };
  return (
    <li>
      <Button variant="link" onClick={() => (open ? setOpen(false) : load())} aria-expanded={open}>
        {project.name}
      </Button>
      {open && (
        <>
          {error && (
            <div role="alert">
              <Button variant="link" onClick={load}>
                Retry project
              </Button>
            </div>
          )}
          {tiers.map((tier) => {
            const initial = (resource?.data as NavigationProjectResource | null)?.[tier];
            const offsets = pages[tier] ?? [];
            const subsequent = offsets.map((offset) =>
              pageResources.get(
                keyID({ kind: "project_page", projectKey: project.key, tier, offset, limit: NAVIGATION_SECTION_LIMIT }),
              ),
            );
            const rows = [
              ...(initial?.sessions ?? []),
              ...subsequent.flatMap(
                (page) => (page?.data as { sessions?: NavigationSessionSummary[] } | null)?.sessions ?? [],
              ),
            ];
            const last = subsequent.at(-1);
            const lastData = last?.data as
              | { remaining?: number; sessions?: NavigationSessionSummary[] }
              | null
              | undefined;
            const remaining: number = lastData?.remaining ?? initial?.remaining ?? 0;
            const offset =
              last && last.key.kind === "project_page"
                ? last.key.offset + (lastData?.sessions?.length ?? 0)
                : (initial?.sessions.length ?? 0);
            return (
              <section key={tier} aria-label={`${project.name} ${tier}`}>
                <h3>{tier}</h3>
                <SessionRows rows={rows} onSelect={onSelect} />
                {!!last?.error && <div role="alert">Page unavailable</div>}
                {remaining > 0 && (
                  <Button
                    variant="link"
                    onClick={() => {
                      setPages((old) => ({
                        ...old,
                        [tier]: (old[tier] ?? []).includes(offset) ? old[tier] : [...(old[tier] ?? []), offset],
                      }));
                      void navigationStore
                        .getState()
                        .loadProjectPage(project.key, tier, offset, NAVIGATION_SECTION_LIMIT);
                    }}
                  >
                    More {tier}
                  </Button>
                )}
              </section>
            );
          })}
        </>
      )}
    </li>
  );
}

export function ComicPicker({ onSelect }: { onSelect(ref: string): void }): React.ReactElement {
  const state = useNavigationStore();
  const [catalogOffsets, setCatalogOffsets] = useState<Partial<Record<Catalog, number[]>>>({});
  const [errors, setErrors] = useState<ReadonlySet<string>>(new Set());
  const sources = selectDisplaySources(state);
  const projects = selectProjectSummaries(state);
  const loadSection = (section: "live" | "needs_you", offset: number) => {
    void navigationStore
      .getState()
      .loadSection(section, offset, NAVIGATION_SECTION_LIMIT)
      .then(
        (result) => {
          setErrors((old) => {
            const next = new Set(old);
            if (result.error) next.add(section);
            else next.delete(section);
            return next;
          });
        },
        () => setErrors((old) => new Set(old).add(section)),
      );
  };
  return (
    <nav aria-label="Conversations">
      <h1>Comic Chat</h1>
      {state.mode === "error" && <div role="alert">Navigation unavailable</div>}
      {(["live", "needs_you"] as const).map((section) => (
        <section key={section} aria-label={section}>
          <h2>{section === "live" ? "Live" : "Needs you"}</h2>
          {sources.length > 0 && (
            <p>
              {sources
                .filter((source) => !source.online)
                .map((source) => `${source.label} unavailable`)
                .join(", ")}
            </p>
          )}
          <SessionRows
            rows={section === "live" ? selectLiveRows(state) : selectNeedsYouRows(state)}
            onSelect={onSelect}
          />
          {errors.has(section) && <div role="alert">Page unavailable</div>}
          {selectSectionRemaining(section, state) > 0 && (
            <Button variant="link" onClick={() => loadSection(section, selectNextSectionOffset(section, state))}>
              More {section}
            </Button>
          )}
          {errors.has(section) && (
            <Button variant="link" onClick={() => loadSection(section, selectNextSectionOffset(section, state))}>
              Retry {section}
            </Button>
          )}
        </section>
      ))}
      {catalogs.map((catalog) => {
        const resources = (catalogOffsets[catalog] ?? [0]).map((offset) =>
          state.resources.get(keyID({ kind: "catalog", catalog, offset, limit: NAVIGATION_CATALOG_LIMIT })),
        );
        const last = resources.at(-1);
        const lastData = last?.data as { remaining?: number; projects?: NavigationProjectSummary[] } | null | undefined;
        const nextOffset =
          last && last.key.kind === "catalog" ? last.key.offset + (lastData?.projects?.length ?? 0) : 0;
        return (
          <section key={catalog} aria-label={catalog}>
            <h2>{catalog === "projects" ? "Projects" : "Archived projects"}</h2>
            <ul>
              {projects
                .filter((project) => !!project.is_archived === (catalog === "archived_projects"))
                .map((project) => (
                  <Project key={project.key} project={project} onSelect={onSelect} />
                ))}
            </ul>
            {!!last?.error && <div role="alert">Catalog unavailable</div>}
            {(!!last?.error || Number(lastData?.remaining ?? 0) > 0) && (
              <Button
                variant="link"
                onClick={() => {
                  const offset = last?.error && last.key.kind === "catalog" ? last.key.offset : nextOffset;
                  setCatalogOffsets((old) => ({
                    ...old,
                    [catalog]: (old[catalog] ?? [0]).includes(offset)
                      ? (old[catalog] ?? [0])
                      : [...(old[catalog] ?? [0]), offset],
                  }));
                  void navigationStore.getState().loadCatalog(catalog, offset, NAVIGATION_CATALOG_LIMIT);
                }}
              >
                {last?.error ? "Retry" : "More"} {catalog}
              </Button>
            )}
          </section>
        );
      })}
    </nav>
  );
}
