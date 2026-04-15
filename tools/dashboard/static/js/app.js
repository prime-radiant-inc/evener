// Serf Experiment Dashboard — Preact SPA shell with hash-based routing.

import { h, render } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ---------------------------------------------------------------------------
// Page loaders — dynamic imports for code splitting
// ---------------------------------------------------------------------------

const pages = {
    experiments:  () => import('./experiments.js'),
    tasks:        () => import('./tasks.js'),
    scoreboard:   () => import('./scoreboard.js'),
    taskHistory:  () => import('./task-history.js'),
    live:         () => import('./live.js'),
    compare:      () => import('./compare.js'),
    runDetail:    () => import('./run-detail.js'),
    taskDetail:   () => import('./task-detail.js'),
    taskStructure: () => import('./task-structure.js'),
};

// ---------------------------------------------------------------------------
// Hash-based router
// ---------------------------------------------------------------------------

function parseQuery(queryString) {
    const out = {};
    if (!queryString) return out;
    for (const pair of queryString.split('&')) {
        const eq = pair.indexOf('=');
        if (eq < 0) continue;
        out[pair.slice(0, eq)] = decodeURIComponent(pair.slice(eq + 1));
    }
    return out;
}

function parseHash(hash) {
    const h = hash || '#/';
    let m;

    // Split path and query: #/foo/bar?x=1 → path="#/foo/bar", query="x=1"
    const qIdx = h.indexOf('?');
    const path = qIdx >= 0 ? h.slice(0, qIdx) : h;
    const query = qIdx >= 0 ? parseQuery(h.slice(qIdx + 1)) : {};
    const trial = query.trial || null;
    const rep = query.rep ? parseInt(query.rep, 10) : null;
    const reps = query.reps
        ? query.reps.split(',').map(n => parseInt(n, 10)).filter(n => !Number.isNaN(n))
        : null;

    // Tasks list
    if (path.match(/^#\/tasks$/)) {
        return { page: 'tasks', params: {} };
    }
    // Task history from tasks view
    if ((m = path.match(/^#\/tasks\/([^/]+)$/))) {
        return { page: 'taskHistory', params: { task: decodeURIComponent(m[1]) } };
    }
    // Task history — must be checked before runDetail
    if ((m = path.match(/^#\/experiments\/tasks\/([^/]+)\/history$/))) {
        return { page: 'taskHistory', params: { task: decodeURIComponent(m[1]) } };
    }
    // Task structure — must be checked before taskDetail
    if ((m = path.match(/^#\/experiments\/([^/]+)\/tasks\/([^/]+)\/structure$/))) {
        return { page: 'taskStructure', params: { runId: decodeURIComponent(m[1]), task: decodeURIComponent(m[2]), trial, rep, reps } };
    }
    // Task detail (new route)
    if ((m = path.match(/^#\/experiments\/([^/]+)\/tasks\/([^/]+)$/))) {
        return { page: 'taskDetail', params: { runId: decodeURIComponent(m[1]), task: decodeURIComponent(m[2]), trial, rep, reps } };
    }
    // Run detail (new route)
    if ((m = path.match(/^#\/experiments\/([^/]+)$/))) {
        return { page: 'runDetail', params: { runId: decodeURIComponent(m[1]) } };
    }
    // Scoreboard
    if (path.match(/^#\/scoreboard$/)) {
        return { page: 'scoreboard', params: {} };
    }
    // Live monitor
    if (path.match(/^#\/live$/)) {
        return { page: 'live', params: {} };
    }
    // Compare
    if (path.match(/^#\/compare$/)) {
        return { page: 'compare', params: {} };
    }
    // Legacy: task detail via #/runs/
    if ((m = path.match(/^#\/runs\/([^/]+)\/tasks\/([^/]+)$/))) {
        return { page: 'taskDetail', params: { runId: decodeURIComponent(m[1]), task: decodeURIComponent(m[2]), trial, rep, reps } };
    }
    // Legacy: run detail via #/runs/
    if ((m = path.match(/^#\/runs\/([^/]+)$/))) {
        return { page: 'runDetail', params: { runId: decodeURIComponent(m[1]) } };
    }
    // Default
    return { page: 'experiments', params: {} };
}

// ---------------------------------------------------------------------------
// useRoute hook
// ---------------------------------------------------------------------------

function useRoute() {
    const [route, setRoute] = useState(() => parseHash(location.hash));

    useEffect(() => {
        const onHashChange = () => setRoute(parseHash(location.hash));
        window.addEventListener('hashchange', onHashChange);
        return () => window.removeEventListener('hashchange', onHashChange);
    }, []);

    return route;
}

// ---------------------------------------------------------------------------
// NavBar
// ---------------------------------------------------------------------------

const navItems = [
    { label: 'Experiments', href: '#/',           page: 'experiments' },
    { label: 'Tasks',       href: '#/tasks',      page: 'tasks' },
    { label: 'Scoreboard',  href: '#/scoreboard', page: 'scoreboard' },
    { label: 'Live',        href: '#/live',        page: 'live' },
    { label: 'Compare',     href: '#/compare',     page: 'compare' },
];

function NavBar({ currentPage }) {
    return html`
        <nav style=${{
            display: 'flex', gap: '4px', padding: '8px 32px',
            background: '#fff', borderBottom: '1px solid rgba(0,0,0,0.06)',
            fontSize: '13px',
        }}>
            ${navItems.map(item => html`
                <a
                    href=${item.href}
                    style=${{
                        padding: '6px 14px', borderRadius: '6px',
                        textDecoration: 'none', fontWeight: 500,
                        color: (currentPage === item.page ||
                               (item.page === 'experiments' && ['runDetail', 'taskDetail', 'taskStructure'].includes(currentPage)) ||
                               (item.page === 'tasks' && currentPage === 'taskHistory'))
                            ? '#1A1A1A' : '#6B6B6B',
                        background: (currentPage === item.page ||
                                    (item.page === 'experiments' && ['runDetail', 'taskDetail', 'taskStructure'].includes(currentPage)) ||
                                    (item.page === 'tasks' && currentPage === 'taskHistory'))
                            ? 'rgba(0,0,0,0.05)' : 'transparent',
                    }}
                >${item.label}</a>
            `)}
        </nav>
    `;
}

// ---------------------------------------------------------------------------
// Breadcrumb
// ---------------------------------------------------------------------------

function Breadcrumb({ items }) {
    if (!items || items.length === 0) return null;
    return html`
        <nav id="breadcrumb" style=${{ display: 'block' }}>
            ${items.map((item, i) => html`
                <span>
                    ${i > 0 && html`<span class="sep">/</span>`}
                    ${i < items.length - 1
                        ? html`<a href=${item.href}>${item.label}</a>`
                        : html`<span class="current">${item.label}</span>`
                    }
                </span>
            `)}
        </nav>
    `;
}

// ---------------------------------------------------------------------------
// PageLoader — lazy-loads a page module, renders it with params
// ---------------------------------------------------------------------------

function PageLoader({ page, params }) {
    const [mod, setMod] = useState(null);
    const [error, setError] = useState(null);
    const [loading, setLoading] = useState(true);
    // Track which page we loaded to detect stale renders
    const [loadedPage, setLoadedPage] = useState(null);

    useEffect(() => {
        setLoading(true);
        setError(null);
        setMod(null);
        setLoadedPage(null);

        const loader = pages[page];
        if (!loader) {
            setError(`Unknown page: ${page}`);
            setLoading(false);
            return;
        }

        loader()
            .then(m => {
                setMod(m);
                setLoadedPage(page);
                setLoading(false);
            })
            .catch(err => {
                setError(err.message || 'Failed to load page');
                setLoading(false);
            });
    }, [page]);

    if (loading) return html`<div class="loading">Loading...</div>`;
    if (error) return html`<div class="error-msg">Error: ${error}</div>`;
    if (!mod || !mod.default) return html`<div class="error-msg">Page module missing default export</div>`;

    const Page = mod.default;
    return html`<${Page} ...${params} />`;
}

// ---------------------------------------------------------------------------
// Breadcrumb builder
// ---------------------------------------------------------------------------

function buildBreadcrumb({ page, params }) {
    const items = [{ label: 'Experiments', href: '#/' }];

    switch (page) {
        case 'tasks':
            return [{ label: 'Tasks', href: '#/tasks' }];
        case 'scoreboard':
            return [{ label: 'Scoreboard', href: '#/scoreboard' }];
        case 'live':
            return [{ label: 'Live', href: '#/live' }];
        case 'compare':
            return [{ label: 'Compare', href: '#/compare' }];
        case 'runDetail':
            items.push({ label: params.runId || 'Run' });
            break;
        case 'taskDetail':
            items.push({ label: params.runId || 'Run', href: `#/experiments/${encodeURIComponent(params.runId)}` });
            items.push({ label: params.task || 'Task' });
            break;
        case 'taskStructure':
            items.push({ label: params.runId || 'Run', href: `#/experiments/${encodeURIComponent(params.runId)}` });
            items.push({ label: params.task || 'Task', href: `#/experiments/${encodeURIComponent(params.runId)}/tasks/${encodeURIComponent(params.task)}${params.trial ? '?trial=' + encodeURIComponent(params.trial) : ''}` });
            items.push({ label: 'Structure' });
            break;
        case 'taskHistory':
            // Check if coming from tasks view or experiments view
            return [
                { label: 'Tasks', href: '#/tasks' },
                { label: params.task || 'Task' },
            ];
        default:
            // experiments list — just the single item
            return items;
    }
    return items;
}

// ---------------------------------------------------------------------------
// App shell
// ---------------------------------------------------------------------------

function App() {
    const { page, params } = useRoute();
    const breadcrumbItems = buildBreadcrumb({ page, params });

    return html`
        <${NavBar} currentPage=${page} />
        <${Breadcrumb} items=${breadcrumbItems} />
        <main id="app">
            <${PageLoader} page=${page} params=${params} />
        </main>
    `;
}

// ---------------------------------------------------------------------------
// Mount
// ---------------------------------------------------------------------------

// Remove the old static breadcrumb nav (Preact renders its own)
const oldNav = document.querySelector('nav#breadcrumb');
if (oldNav) oldNav.remove();

// Mount into a wrapper that replaces the original #app
const appEl = document.getElementById('app');
const wrapper = document.createElement('div');
wrapper.id = 'app-root';
appEl.parentNode.insertBefore(wrapper, appEl);
appEl.remove();

render(html`<${App} />`, wrapper);
