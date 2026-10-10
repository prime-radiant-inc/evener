import assert from "node:assert/strict";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, connectOnlyPage, evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const artifacts = process.env.DOCUMENT_FILE_LINKS_ARTIFACT_DIR;
assert(artifacts, "Go fixture must provide a retained artifact directory");
mkdirSync(artifacts, { recursive: true });
const cases = [];
let guard;
let page;
async function until(send, expression) {
  return evaluate(send, `(async () => {
    const deadline = performance.now() + 15000;
    for (;;) {
      const result = (${expression});
      if (result) return result;
      if (performance.now() > deadline) throw new Error(${JSON.stringify(expression)});
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  })()`);
}
async function click(send, selector) {
  const point = await evaluate(send, `(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) throw new Error('missing click target');
    // An inline filename can wrap, its bounding box center can be whitespace.
    const r = el.getClientRects()[0];
    const point = { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    return { ...point, hit: document.elementFromPoint(point.x, point.y)?.outerHTML, target: el.outerHTML };
  })()`);
  console.log(JSON.stringify({ click: selector, ...point }));
  const { x, y } = point;
  await send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", clickCount: 1, x, y });
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", clickCount: 1, x, y });
}
try {
  guard = await startBrowserGuard({ frontend, profilePrefix: "documentfilelinks-chrome-" });
  const endpoint = await waitForBrowserReady(guard);
  page = await connectOnlyPage(endpoint);
  await page.send("Runtime.enable");
  page.ws.addEventListener("message", event => {
    const frame = JSON.parse(event.data);
    if (frame.method === "Runtime.exceptionThrown" || frame.method === "Runtime.consoleAPICalled") {
      console.error(JSON.stringify(frame));
    }
  });
  await applyViewport(page.send, { width: 1400, height: 900 });
  // Vite's existing /doc proxy also matches /documentfilelinksharness.html.
  // Its explicit filesystem route serves this owned dev page without changing
  // the production proxy or bypassing it for document HTTP requests.
  const url = new URL(`http://127.0.0.1:${guard.vitePort}/@fs/${frontend}/documentfilelinksharness.html`);
  url.searchParams.set("cwd", process.env.DOCUMENT_FILE_LINKS_CWD);
  url.searchParams.set("ref", process.env.DOCUMENT_FILE_LINKS_REF);
  await navigateTo(page, `${url.origin}/auth/${process.env.DOCUMENT_FILE_LINKS_TOKEN}`);
  await navigateTo(page, url.href);
  await until(page.send, "document.querySelectorAll('[data-file-links-source] a[href^=\"/doc/file\"]').length >= 2");
  await until(page.send, "!!window.fileLinksFixture.route().location?.data");
  writeFileSync(path.join(artifacts, 'route-before.json'), JSON.stringify(await evaluate(page.send, 'window.fileLinksFixture.route()'), null, 2));
  await evaluate(page.send, `(() => {
    window.fileLinksClickEvidence = [];
    document.addEventListener('click', event => {
      const row = { trusted: event.isTrusted, button: event.button, ctrl: event.ctrlKey, meta: event.metaKey, shift: event.shiftKey, alt: event.altKey, target: event.target.outerHTML };
      window.fileLinksClickEvidence.push(row);
      requestAnimationFrame(() => { row.prevented = event.defaultPrevented; });
    }, true);
  })()`);
  assert.equal(await evaluate(page.send, "window.fileLinksFixture.requests()"), 0, "recognition must not read documents");
  cases.push("recognition performs no document reads");
  const originalSource = await evaluate(page.send, "window.fileLinksFixture.state().panes.find(pane => pane.type === 'session')");
  await evaluate(page.send, "window.fileLinksOriginalSource = document.querySelector('[data-file-links-source]'); true");
  for (const [index, expected] of [[0, "SPEC CURRENT FILE"], [1, "REVIEW CURRENT FILE"]]) {
    const suffix = index === 0 ? "design.md" : "review.md";
    const listenerTarget = await page.send('Runtime.evaluate', { expression: `document.querySelector('[data-file-links-source] a[href$="${suffix}"]')` });
    console.log(JSON.stringify({ index, listeners: await page.send('DOMDebugger.getEventListeners', { objectId: listenerTarget.result.result.objectId }), before: await evaluate(page.send, "window.fileLinksFixture.state()") }));
    await click(page.send, `[data-file-links-source] a[href$="${suffix}"]`);
    await until(page.send, `document.querySelector('[data-file-links-document="' + window.fileLinksFixture.state().focusedPaneId + '"]')?.textContent.includes(${JSON.stringify(expected)})`);
    const geometry = await evaluate(page.send, `(() => {
      const source = document.querySelector('[data-file-links-source]');
      const viewer = document.querySelector('[data-file-links-document="' + window.fileLinksFixture.state().focusedPaneId + '"]');
      if (!source || !viewer) throw new Error('source or document is missing');
      const a = source.getBoundingClientRect(); const b = viewer.getBoundingClientRect();
      return { sourceRight: a.right, documentLeft: b.left, sourceWidth: a.width, documentWidth: b.width };
    })()`);
    assert(geometry.sourceWidth > 0 && geometry.documentWidth > 0);
    assert(geometry.documentLeft >= geometry.sourceRight);
    const state = await evaluate(page.send, "window.fileLinksFixture.state()");
    const document = state.panes.find(pane => pane.id === state.focusedPaneId);
    assert.deepEqual(state.panes.find(pane => pane.id === originalSource.id), originalSource);
    assert.equal(document.params.session, process.env.DOCUMENT_FILE_LINKS_REF);
    assert.equal(document.params.path, `docs/superpowers/specs/2026-10-02-web-session-overview-${index === 0 ? "design" : "review"}.md`);
    assert.equal(document.document.reference.readTarget, `${process.env.DOCUMENT_FILE_LINKS_CWD}/${document.params.path}`);
    assert.deepEqual(document.document.origin, originalSource);
    assert(await evaluate(page.send, "window.fileLinksOriginalSource === document.querySelector('[data-file-links-source]')"));
    assert.equal(await evaluate(page.send, "document.querySelectorAll('.dv-groupview').length"), 2, "no third column");
    const documents = state.panes.filter(pane => pane.type === 'doc');
    assert.equal(new Set(documents.map(pane => JSON.stringify(pane.params))).size, documents.length, "no duplicate document identity");
    console.log(JSON.stringify({ expected, geometry, state, requests: await evaluate(page.send, "window.fileLinksFixture.requests()") }));
    cases.push(`exact example ${index + 1}, actual HTTP bytes and desktop geometry`);
  }
  const htmlBoundaries = [
  {
    "name": "standalone-a",
    "markdown": "<a href=\"https://example.test/x\">\n\ndocs/private.md\n\n</a>",
    "paths": [
      "docs/private.md"
    ]
  },
  {
    "name": "inline-a-paragraphs",
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md </a> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-a",
    "markdown": "Before <a> docs/a.md </a> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-a",
    "markdown": "<a>\n docs/a.md\n</a>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-div",
    "markdown": "Before <div> docs/a.md </div> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-div",
    "markdown": "<div>\n docs/a.md\n</div>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-span",
    "markdown": "Before <span> docs/a.md </span> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-span",
    "markdown": "<span>\n docs/a.md\n</span>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-script",
    "markdown": "Before <script> docs/a.md </script> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-script",
    "markdown": "<script>\n docs/a.md\n</script>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-style",
    "markdown": "Before <style> docs/a.md </style> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-style",
    "markdown": "<style>\n docs/a.md\n</style>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-pre",
    "markdown": "Before <pre> docs/a.md </pre> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-pre",
    "markdown": "<pre>\n docs/a.md\n</pre>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "inline-code",
    "markdown": "Before <code> docs/a.md </code> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "block-code",
    "markdown": "<code>\n docs/a.md\n</code>",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "comment-inline",
    "markdown": "Before <!-- docs/a.md --> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "comment-block",
    "markdown": "<!--\ndocs/a.md\n-->",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "unclosed-script",
    "markdown": "Before <script> first\n\n docs/a.md",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "unclosed-a",
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "malformed-tag",
    "markdown": "Before <a href= docs/a.md after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "table",
    "markdown": "| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/b.md </script> | docs/c.md |",
    "paths": [
      "docs/a.md",
      "docs/b.md",
      "docs/c.md"
    ]
  },
  {
    "name": "list",
    "markdown": "- Before <a href=\"https://example.test/x\"> first\n- docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "name": "blockquote-crlf",
    "markdown": "> Before <pre> first\r\n>\r\n> docs/a.md </pre> docs/b.md\r\n",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "name": "quoted-attribute",
    "markdown": "Before <a href=\"https://example.test/docs/url.md\" data-file=\"docs/attribute.md\"> docs/body.md </a> after",
    "paths": [
      "docs/attribute.md",
      "docs/body.md"
    ]
  },
  {
    "name": "unsafe-attributes",
    "markdown": "Before <a href=\"https://example.test/docs/url.md\" data-file=\"../docs/no.md\" data-other=\"mailto:a/docs/no.md\"> docs/body.md </a> after",
    "paths": [
      "docs/body.md"
    ]
  },
  {
    "name": "tag-adjacent",
    "markdown": "Before <code>docs/a.md</code> after",
    "paths": []
  },
  {
    "name": "closing-boundary",
    "markdown": "Before <a> docs/a.md </a>docs/b.md",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "name": "invalid-boundaries",
    "markdown": "Before <script> ../docs/a.md https://host/docs/b.md </script> ../**docs/c.md**",
    "paths": []
  },
  {
    "name": "actual-links",
    "markdown": "Before <a> [docs/label.md](https://example.test/x) [R](./docs/file.md) </a> after",
    "paths": [
      "docs/file.md"
    ]
  },
  {
    "name": "inline-code",
    "markdown": "Before <a> `README.md` </a> after",
    "paths": [
      "README.md"
    ]
  },
  {
    "name": "fences",
    "markdown": "<a>\n\n```sh\ndocs/no.md\n```\n\n```mermaid\ndocs/no.md\n```\n\n docs/yes.md </a>",
    "paths": [
      "docs/yes.md"
    ]
  },
  {
    "name": "literal-entity-percent",
    "markdown": "Before <!-- docs/a&amp;b.md docs/100%25.md -->",
    "paths": [
      "docs/a&amp;b.md",
      "docs/100%25.md"
    ]
  }
];
  htmlBoundaries.push({"name": "literal-html-escaped-table-header", "markdown": "Before <script> first\n\n| \\|docs/a.md | Why |\n| --- | --- |\n| </script> ../**docs/x.md** \\|docs/b.md | docs/c.md |", "paths": ["|docs/a.md", "|docs/b.md", "docs/c.md"]});
  // Explicit platform expectations, native keeps Markdown contexts while web
  // continues to display the authored block as literal text.
  const mixedBlocks = [
  {
    "name": "mixed-file-destination",
    "markdown": "<div>\n[R](./docs/file.md)\n</div>",
    "paths": [
      "docs/file.md"
    ],
    "webPaths": [
      "docs/file.md"
    ],
    "rendered": "<div>\n[R](action-0)\n</div>"
  },
  {
    "name": "mixed-fenced-code",
    "markdown": "<script>\n```sh\ndocs/a.md\n```\n</script>",
    "paths": [],
    "webPaths": [
      "docs/a.md"
    ]
  },
  {
    "name": "mixed-inline-code",
    "markdown": "<div>\n`docs/a.md`\n</div>",
    "paths": [
      "docs/a.md"
    ],
    "webPaths": [
      "`docs/a.md`"
    ],
    "rendered": "<div>\n[`docs/a.md`](action-0)\n</div>"
  },
  {
    "name": "mixed-external-label",
    "markdown": "<div>\n[docs/label.md](https://example.test/x)\n</div>",
    "paths": [],
    "webPaths": [
      "docs/label.md"
    ],
    "externalDestinations": [
      "https://example.test/x"
    ]
  },
  {
    "name": "mixed-indented-continuation",
    "markdown": "<div>\n    docs/a.md\n</div>",
    "paths": [
      "docs/a.md"
    ],
    "webPaths": [
      "docs/a.md"
    ],
    "rendered": "<div>\n    [docs/a.md](action-0)\n</div>"
  },
  {
    "name": "mixed-indented-code",
    "markdown": "<div>\n\n    docs/a.md\n\n</div>",
    "paths": [],
    "webPaths": []
  },
  {
    "name": "mixed-mermaid-code",
    "markdown": "<style>\n```mermaid\ndocs/a.md\n```\n</style>",
    "paths": [],
    "webPaths": [
      "docs/a.md"
    ]
  },
  {
    "name": "mixed-file-label",
    "markdown": "<pre>\n[docs/label.md `]`](<./docs/a%20b.md> \"keep\")\n</pre>",
    "paths": [
      "docs/a b.md"
    ],
    "webPaths": [
      "docs/label.md"
    ],
    "rendered": "<pre>\n[docs/label.md `]`](<action-0> \"keep\")\n</pre>"
  },
  {
    "name": "mixed-local-definition",
    "markdown": "<script>\n[R][r]\n\n[r]: ./docs/a.md \"keep\"\n</script>",
    "paths": [
      "docs/a.md"
    ],
    "webPaths": [
      "docs/a.md"
    ],
    "rendered": "<script>\n[R](action-0)\n\n[r]: ./docs/a.md \"keep\"\n</script>"
  },
  {
    "name": "mixed-forward-reference",
    "markdown": "<div>\n[R][r]\n</div>\n\n[r]: ./docs/a.md \"keep\"",
    "paths": [
      "docs/a.md"
    ],
    "webPaths": [],
    "rendered": "<div>\n[R](action-0)\n</div>\n\n[r]: ./docs/a.md \"keep\""
  },
  {
    "name": "mixed-blockquote-crlf",
    "markdown": "> <div>\r\n> [R](./docs/file.md)\r\n> </div>\r\n",
    "paths": [
      "docs/file.md"
    ],
    "webPaths": [
      "docs/file.md"
    ],
    "rendered": "> <div>\r\n> [R](action-0)\r\n> </div>\r\n"
  },
  {
    "name": "mixed-list-fence",
    "markdown": "- <script>\n  ```sh\n  docs/a.md\n  ```\n  </script>",
    "paths": [],
    "webPaths": [
      "docs/a.md"
    ]
  },
  {
    "name": "mixed-inline-command",
    "markdown": "<pre>\n`cat docs/a.md`\n</pre>",
    "paths": [],
    "webPaths": [
      "docs/a.md`"
    ]
  }
];
  const webMixed = [];
  for (const sample of mixedBlocks) {
    webMixed.push({ ...sample, actual: await evaluate(page.send, `window.fileLinksFixture.qualifyMarkdown(${JSON.stringify(sample.markdown)}, '/work/tree')`) });
  }
  writeFileSync(path.join(artifacts, "web-mixed-blocks.json"), JSON.stringify(webMixed, null, 2));
  for (const sample of webMixed) assert.deepEqual(sample.actual.paths, sample.webPaths, `${sample.name}, explicit web literal-text expectation`);
  cases.push(...webMixed.map(sample => `${sample.name}, explicit web literal-text paths, native Markdown difference approved`));
  const webHtml = [];
  for (const sample of htmlBoundaries) {
    webHtml.push({ ...sample, actual: await evaluate(page.send, `window.fileLinksFixture.qualifyMarkdown(${JSON.stringify(sample.markdown)}, '/work/tree')`) });
  }
  writeFileSync(path.join(artifacts, "html-versions.json"), JSON.stringify({
    browser: await page.send("Browser.getVersion"),
    marked: JSON.parse(readFileSync(path.join(frontend, "node_modules/marked/package.json"), "utf8")).version,
    dompurify: JSON.parse(readFileSync(path.join(frontend, "node_modules/dompurify/package.json"), "utf8")).version,
    renderer: "production widgets/markdown, authored HTML escaped then DOMPurify sanitized",
  }, null, 2));
  writeFileSync(path.join(artifacts, "web-html-boundary.json"), JSON.stringify(webHtml, null, 2));
  const historicalInputs = [
  {
    "markdown": "```mermaid\ndocs/a.md\n```\n\n```sh\ndocs/b.md\n```\n\n    docs/c.md\n\n<div>docs/d.md</div>\n\n[web](https://example.test/docs/e.md)",
    "paths": []
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> docs/a.md </a> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> `README.md` [R](./docs/a.md) </a> after",
    "paths": [
      "README.md",
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> **docs/a.md *`README.md`* [R](./docs/b.md)** </a> after",
    "paths": [
      "docs/a.md",
      "README.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before **<a href=\"https://example.test/x\"> docs/a.md** `README.md` </a> after",
    "paths": [
      "docs/a.md",
      "README.md"
    ]
  },
  {
    "markdown": "Before <script> docs/a.md `README.md` [R](./docs/b.md) </script> after",
    "paths": [
      "docs/a.md",
      "README.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <pre> docs/a.md </pre> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "docs/b.md <a href=\"https://example.test/x\"> docs/a.md </a>\"docs/c.md\"",
    "paths": [
      "docs/b.md",
      "docs/a.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "docs/b.md <a href=\"https://example.test/x\"> docs/a.md </a> docs/c.md",
    "paths": [
      "docs/b.md",
      "docs/a.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "docs/b.md <script> docs/a.md </script> docs/c.md",
    "paths": [
      "docs/b.md",
      "docs/a.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "docs/b.md <a href=\"https://example.test/x\"> docs/a.md </a>docs/c.md",
    "paths": [
      "docs/b.md",
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> docs/a.md </a>foo`README.md` after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md </a> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n docs/a.md </script> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <pre> first\n\n docs/a.md </pre> after",
    "paths": [
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\r\n\r\n docs/a.md </a> docs/b.md\r\n",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <script> first\r\n\r\n docs/a.md </script> docs/b.md\r\n",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before **<a href=\"https://example.test/x\"> first**\n\n *docs/a.md </a>* docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "> Before <a href=\"https://example.test/x\"> first\n>\n> docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "- Before <a href=\"https://example.test/x\"> first\n\n  docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "- Before <a href=\"https://example.test/x\"> first\n- docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n# docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n> docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "> Before <a href=\"https://example.test/x\"> first\n\n docs/a.md </a> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n| docs/a.md </script> docs/b.md | after |\n| --- | --- |",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n# docs/a.md\n\n<pre>docs/a.md</pre>\n\n docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n```sh\n</script> docs/a.md\n```\n\n docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "| Before <a href=\"https://example.test/x\"> first | docs/a.md </a> docs/b.md |\n| --- | --- |\n| docs/c.md | after |",
    "paths": [
      "docs/a.md",
      "docs/b.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/a.md </script> docs/b.md | docs/c.md |",
    "paths": [
      "docs/a.md",
      "docs/a.md",
      "docs/b.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "> | Before <pre> first | docs/a.md |\r\n> | --- | --- |\r\n> | docs/a.md </pre> docs/b.md | docs/c.md |\r\n",
    "paths": [
      "docs/a.md",
      "docs/a.md",
      "docs/b.md",
      "docs/c.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md `README.md` [R](./docs/a.md)",
    "paths": [
      "docs/a.md",
      "README.md",
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n docs/a.md `README.md` [R](./docs/a.md)",
    "paths": [
      "docs/a.md",
      "README.md",
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <pre> first\n\n# docs/a.md\n\n docs/a.md",
    "paths": [
      "docs/a.md",
      "docs/a.md"
    ]
  },
  {
    "markdown": "Before <script> first\n\n docs/a.md </script> ../**docs/a.md** docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <pre> first\n\n docs/a.md </pre> https://host/**docs/a.md** docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md </a>foo`README.md` docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "[Before <script> first](https://example.test/x)\n\n docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "![Before <script> first](./image.png)\n\n docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "[Before **<script> first**](https://example.test/x) docs/a.md </script> docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n[inner](./docs/a.md) docs/b.md",
    "paths": [
      "docs/a.md",
      "docs/b.md"
    ]
  },
  {
    "markdown": "Before <a href=\"https://example.test/x\"> first\n\n docs/a.md",
    "paths": [
      "docs/a.md"
    ]
  }
];
  const historicalHtml = [];
  for (const sample of historicalInputs) historicalHtml.push({ ...sample, actual: await evaluate(page.send, `window.fileLinksFixture.qualifyMarkdown(${JSON.stringify(sample.markdown)}, '/work/tree')`) });
  writeFileSync(path.join(artifacts, "web-historical-html.json"), JSON.stringify(historicalHtml, null, 2));
  for (const sample of historicalHtml) assert.deepEqual(sample.actual.paths, sample.paths, sample.markdown);
  const entityDiscrepancies = [];
  for (const markdown of ["Before <code> docs/a&amp;b.md </code>", "Before docs/a&amp;b.md after"]) {
    const actual = await evaluate(page.send, `window.fileLinksFixture.qualifyMarkdown(${JSON.stringify(markdown)}, '/work/tree')`);
    entityDiscrepancies.push(actual);
  }
  writeFileSync(path.join(artifacts, "web-entity-discrepancy.json"), JSON.stringify(entityDiscrepancies, null, 2));
  for (const sample of entityDiscrepancies) assert.deepEqual(sample.paths, ["docs/a&b.md"], "inherited web entity interpretation, not HTML parity");
  for (const sample of webHtml) assert.deepEqual(sample.actual.paths, sample.paths, sample.name);
  cases.push(...webHtml.map(sample => `literal HTML ${sample.name}, sanitized DOM paths`));
  await until(page.send, "!!document.querySelector('[data-file-links-source] .ProseMirror[contenteditable=\"true\"]')");
  await click(page.send, '[data-file-links-source] .ProseMirror[contenteditable="true"]');
  const draftText = "Retained parent draft from real browser input";
  await page.send("Input.insertText", { text: draftText });
  await until(page.send, `JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)}))?.text === ${JSON.stringify(draftText)}`);
  const draftBefore = await evaluate(page.send, `({ editor: document.querySelector('[data-file-links-source] .ProseMirror').textContent,
    stored: JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)})) })`);
  assert.equal(draftBefore.editor, draftText);
  writeFileSync(path.join(artifacts, "draft-before.json"), JSON.stringify(draftBefore, null, 2));
  await click(page.send, '[data-file-links-source] a[href$="review.md"]');
  await until(page.send, "document.querySelector('[data-file-links-document=\"' + window.fileLinksFixture.state().focusedPaneId + '\"]')?.textContent.includes('REVIEW CURRENT FILE')");
  const retainedDocument = await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId");
  await applyViewport(page.send, { width: 940, height: 900 });
  const wrapped = await until(page.send, `(() => {
    const link = document.querySelector('[data-file-links-source] a[href$="design.md"]');
    const rects = [...link.getClientRects()].map(r => ({ left: r.left, right: r.right, top: r.top }));
    return new Set(rects.map(r => r.top)).size > 1 && rects;
  })()`);
  assert(wrapped.every(r => r.left >= 0 && r.right <= 940), "wrapped filename stays in viewport");
  cases.push("narrow desktop filename wraps without viewport overflow");
  await applyViewport(page.send, { width: 390, height: 844 });
  await until(page.send, "!!(document.querySelector('[aria-label=\"Back\"]') && document.querySelector('[data-file-links-document]'))");
  const phone = await evaluate(page.send, `(() => {
    const r = document.querySelector('[data-file-links-document]').getBoundingClientRect();
    return { left: r.left, right: r.right, width: r.width, height: r.height,
      sourceCount: document.querySelectorAll('[data-file-links-source]').length };
  })()`);
  assert.equal(phone.sourceCount, 0, "phone document is the only mounted pane");
  assert(phone.left >= 0 && phone.right <= 390 && phone.width >= 389 && phone.height > 700);
  assert.equal(await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId"), retainedDocument);
  await click(page.send, '[aria-label="Back"]');
  await until(page.send, `window.fileLinksFixture.state().focusedPaneId === ${JSON.stringify(originalSource.id)} && !!document.querySelector('[data-file-links-source] a[href$="design.md"]')`);
  cases.push("phone full-screen document, breakpoint preserves identity, actual top-bar Back returns exact source");
  let keyboardFocused = false;
  for (let n = 0; n < 60 && !keyboardFocused; n++) {
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 });
    keyboardFocused = await evaluate(page.send, "document.activeElement?.matches('[data-file-links-source] a[href$=\"design.md\"]')");
  }
  assert(keyboardFocused, "real Tab reaches filename");
  const focus = await evaluate(page.send, `(() => {
    const s = getComputedStyle(document.activeElement);
    return { visible: document.activeElement.matches(':focus-visible'), outline: s.outlineStyle, width: s.outlineWidth };
  })()`);
  assert(focus.visible && focus.outline !== "none" && focus.width !== "0px", "keyboard filename focus is visible");
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
  await until(page.send, "document.querySelector('[data-file-links-document]')?.textContent.includes('SPEC CURRENT FILE')");
  const keyboardDocument = await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId");
  const beforeBreakpoint = await evaluate(page.send, `({ state: window.fileLinksFixture.state(),
    stored: JSON.parse(localStorage.getItem('evener.workspace.layout.v2')),
    viewer: document.querySelector('[data-file-links-document]')?.textContent })`);
  writeFileSync(path.join(artifacts, "breakpoint-before.json"), JSON.stringify(beforeBreakpoint, null, 2));
  await applyViewport(page.send, { width: 1400, height: 900 });
  await until(page.send, "document.querySelectorAll('.dv-groupview').length === 2 && !!document.querySelector('[data-file-links-source]')");
  const afterBreakpoint = await evaluate(page.send, `(async () => {
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    return { state: window.fileLinksFixture.state(), layout: window.fileLinksFixture.state().layoutJSON(),
      viewers: [...document.querySelectorAll('[data-file-links-document]')].map(el => ({ id: el.dataset.fileLinksDocument,
        text: el.textContent, width: el.getBoundingClientRect().width })) };
  })()`);
  writeFileSync(path.join(artifacts, "breakpoint-after.json"), JSON.stringify(afterBreakpoint, null, 2));
  writeFileSync(path.join(artifacts, "phone-keyboard.json"), JSON.stringify({ phone, wrapped, focus, keyboardDocument }, null, 2));
  for (const prior of beforeBreakpoint.state.panes.filter(pane => pane.type === "doc")) {
    const restored = afterBreakpoint.state.panes.find(pane => pane.id === prior.id);
    assert.deepEqual(restored?.document?.origin, prior.document.origin, "host swap preserves latest exact Back source");
  }
  assert.equal(await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId"), keyboardDocument);
  assert.equal(await evaluate(page.send, "window.fileLinksFixture.state().panes.filter(p => p.type === 'doc').length"), 2, "reopen does not duplicate document");
  cases.push("real Tab, visible focus and Enter opens HTTP file, phone→desktop preserves document");
  const draftAfter = await evaluate(page.send, `({ editor: document.querySelector('[data-file-links-source] .ProseMirror').textContent,
    stored: JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)})) })`);
  writeFileSync(path.join(artifacts, "draft-after.json"), JSON.stringify(draftAfter, null, 2));
  assert.deepEqual(draftAfter, draftBefore, "genuine parent editor input and persisted draft survive both host changes");
  cases.push("real ProseMirror input and persisted parent draft retained across desktop→phone→desktop");
  const pixels = async () => until(page.send, `(() => {
    const img = document.querySelector('[data-file-links-document="' + window.fileLinksFixture.state().focusedPaneId + '"] img[data-testid="doc-image"]');
    if (!img?.complete || img.naturalWidth !== 2) return false;
    const canvas = document.createElement('canvas'); canvas.width = 2; canvas.height = 2;
    const ctx = canvas.getContext('2d'); ctx.drawImage(img, 0, 0);
    return { rgba: [...ctx.getImageData(0, 0, 1, 1).data], src: img.getAttribute('src'), width: img.naturalWidth };
  })()`);
  await click(page.send, '[data-file-links-source] a[href*="current.png"]');
  const imagePane = await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId");
  const red = await pixels();
  assert.deepEqual(red.rgba, [255, 0, 0, 255], "authenticated real PNG decodes red");
  const mutationStarted = Date.now();
  await evaluate(page.send, "fetch('/doc/fixture/image?color=blue', { method: 'POST' }).then(r => { if (!r.ok) throw new Error('image mutation failed'); return true; })");
  await click(page.send, '[data-file-links-source] a[href*="current.png"]');
  await until(page.send, `document.querySelector('[data-file-links-document="${imagePane}"] img')?.getAttribute('src') !== ${JSON.stringify(red.src)}`);
  const blue = await pixels();
  assert.deepEqual(blue.rgba, [0, 0, 255, 255], "explicit reopen decodes current blue bytes at same path");
  assert.equal(await evaluate(page.send, "window.fileLinksFixture.state().focusedPaneId"), imagePane, "reopen keeps image pane identity");
  await evaluate(page.send, "fetch('/doc/fixture/image?color=red', { method: 'POST' }).then(r => { if (!r.ok) throw new Error('image mutation failed'); return true; })");
  const reloadSelector = await evaluate(page.send, `(() => {
    const buttons = [...document.querySelectorAll('[data-file-links-document="${imagePane}"] button')];
    const index = buttons.findIndex(b => b.textContent.trim() === 'Reload');
    if (index < 0) throw new Error('actual Reload button missing');
    return '[data-file-links-document="${imagePane}"] button:nth-of-type(' + (index + 1) + ')';
  })()`);
  await click(page.send, reloadSelector);
  await until(page.send, `document.querySelector('[data-file-links-document="${imagePane}"] img')?.getAttribute('src') !== ${JSON.stringify(blue.src)}`);
  const reloaded = await pixels();
  writeFileSync(path.join(artifacts, "image-freshness.json"), JSON.stringify({ red, blue, reloaded, elapsedMs: Date.now() - mutationStarted }, null, 2));
  assert.deepEqual(reloaded.rgba, [255, 0, 0, 255], "actual Reload decodes replacement red bytes");
  assert(Date.now() - mutationStarted < 60000, "freshness proven inside existing cache lifetime");
  cases.push("real authenticated image decode, same-path red→blue reopen and actual Reload within60seconds");
  await click(page.send, '[data-file-links-source] .ProseMirror[contenteditable="true"]');
  const scrollExpression = `(() => {
    const source = document.querySelector('[data-file-links-source]');
    const el = [...source.querySelectorAll('*')].find(el =>
      ['auto', 'scroll'].includes(getComputedStyle(el).overflowY) && el.scrollHeight > el.clientHeight + 100);
    if (!el) throw new Error('actual scrollable transcript missing');
    const r = el.getBoundingClientRect();
    return { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight,
      x: r.left + r.width / 2, y: r.top + r.height / 2, className: el.className };
  })()`;
  const atEnd = await evaluate(page.send, scrollExpression);
  await page.send('Input.dispatchMouseEvent', { type: 'mouseWheel', x: atEnd.x, y: atEnd.y, deltaX: 0, deltaY: -420 });
  const scrolled = await until(page.send, `(() => { const value = ${scrollExpression}; return value.top < ${atEnd.top} - 100 && value; })()`);
  assert(scrolled.top > 0 && scrolled.top < scrolled.height - scrolled.client - 100, 'genuine wheel moved away from both boundaries');
  writeFileSync(path.join(artifacts, 'scroll-before.json'), JSON.stringify({ atEnd, scrolled }, null, 2));
  await applyViewport(page.send, { width: 390, height: 844 });
  await until(page.send, 'document.querySelectorAll(".dv-groupview").length === 0 && !!document.querySelector("[data-file-links-source] .ProseMirror")');
  const phoneScroll = await evaluate(page.send, scrollExpression);
  await applyViewport(page.send, { width: 1400, height: 900 });
  await until(page.send, 'document.querySelectorAll(".dv-groupview").length === 2 && !!document.querySelector("[data-file-links-source] .ProseMirror")');
  const restoredScroll = await evaluate(page.send, `(async () => {
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    return ${scrollExpression};
  })()`);
  writeFileSync(path.join(artifacts, 'scroll-after.json'), JSON.stringify({ phoneScroll, restoredScroll }, null, 2));
  assert(Math.abs(restoredScroll.top - scrolled.top) <= 2, 'same desktop viewport retains genuine transcript scroll through both host swaps');
  assert.deepEqual(await evaluate(page.send, `JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)}))`), draftBefore.stored);
  cases.push('genuine transcript wheel scrolling and draft survive both host swaps');

  // Q1 starts only after every inherited strict oracle above has run.
  const recoveredPath = "docs/recovered.md";
  const recoveredTarget = `${process.env.DOCUMENT_FILE_LINKS_CWD}/${recoveredPath}`;
  const recoveredContents = "Q1 RECOVERED CURRENT FILE\n";
  assert(!existsSync(recoveredTarget), "Q1 starts with a genuinely absent file");
  const missingSelector = '[data-file-links-source] a[href$="recovered.md"]';
  const q1StartScroll = await evaluate(page.send, scrollExpression);
  await page.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: q1StartScroll.x, y: q1StartScroll.y,
    deltaX: 0, deltaY: q1StartScroll.height });
  await until(page.send, `(() => {
    const anchor = document.querySelector(${JSON.stringify(missingSelector)});
    const r = anchor?.getClientRects()[0];
    return r && r.top >= 0 && r.bottom <= innerHeight &&
      anchor.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
  })()`);
  await evaluate(page.send, `(() => {
    const anchor = document.querySelector(${JSON.stringify(missingSelector)});
    if (!anchor) throw new Error('actual assistant missing-file anchor absent');
    window.fileLinksQ1Source = document.querySelector('[data-file-links-source]');
    window.fileLinksQ1Clicks = [];
    document.addEventListener('click', event => {
      const anchor = event.target.closest?.(${JSON.stringify(missingSelector)});
      const button = event.target.closest?.('[data-file-links-document] button');
      if (!anchor && button?.textContent.trim() !== 'Reload') return;
      window.fileLinksQ1Clicks.push({ trusted: event.isTrusted, button: event.button,
        ctrl: event.ctrlKey, meta: event.metaKey, shift: event.shiftKey, alt: event.altKey,
        href: anchor?.getAttribute('href'), text: (anchor ?? button).textContent,
        target: (anchor ?? button).outerHTML, at: performance.now() });
    }, true);
    return true;
  })()`);
  const q1Requests = [];
  const q1BrowserLogs = [];
  const q1Extra = new Map();
  const q1Waiters = new Set();
  const q1NetworkPath = path.join(artifacts, "q1-network.json");
  const saveQ1Network = () => writeFileSync(q1NetworkPath, JSON.stringify(q1Requests, null, 2));
  const q1NetworkListener = event => {
    const frame = JSON.parse(event.data);
    const params = frame.params;
    if (!params) return;
    if (frame.method === "Log.entryAdded") {
      q1BrowserLogs.push(params.entry);
      writeFileSync(path.join(artifacts, "q1-browser-console.json"), JSON.stringify(q1BrowserLogs, null, 2));
      console.error(JSON.stringify(frame));
    }
    if (frame.method === "Network.requestWillBeSentExtraInfo") {
      const credentials = { cookieHeaderPresent: Object.keys(params.headers).some(name => name.toLowerCase() === "cookie"),
        cookieNames: params.associatedCookies.filter(cookie => cookie.blockedReasons.length === 0).map(cookie => cookie.cookie.name) };
      q1Extra.set(params.requestId, credentials);
      const request = q1Requests.find(request => request.requestId === params.requestId);
      if (request) { request.credentials = credentials; saveQ1Network(); }
    }
    if (frame.method === "Network.requestWillBeSent") {
      const url = new URL(params.request.url);
      if (url.pathname !== "/doc/file" || url.searchParams.get("path") !== recoveredTarget) return;
      q1Requests.push({ requestId: params.requestId, url: params.request.url, method: params.request.method,
        initiator: params.initiator.type, at: params.timestamp, credentials: q1Extra.get(params.requestId) });
      saveQ1Network();
    }
    const request = q1Requests.find(request => request.requestId === params.requestId);
    if (!request) return;
    if (frame.method === "Network.responseReceived") {
      request.response = { status: params.response.status, url: params.response.url, mimeType: params.response.mimeType,
        fromDiskCache: params.response.fromDiskCache, fromServiceWorker: params.response.fromServiceWorker,
        contentType: params.response.headers["Content-Type"] ?? params.response.headers["content-type"] };
      saveQ1Network();
    }
    if (frame.method === "Network.loadingFinished") {
      page.send("Network.getResponseBody", { requestId: params.requestId }).then(frame => {
        request.body = frame.result.base64Encoded ? Buffer.from(frame.result.body, "base64").toString("utf8") : frame.result.body;
        request.finishedAt = params.timestamp;
        saveQ1Network();
        for (const waiter of q1Waiters) waiter();
      }).catch(error => {
        request.captureError = error.message;
        saveQ1Network();
        for (const waiter of q1Waiters) waiter();
      });
    }
  };
  page.ws.addEventListener("message", q1NetworkListener);
  await page.send("Network.enable");
  await page.send("Log.enable");
  const q1Response = (status, firstIndex) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      q1Waiters.delete(check);
      reject(new Error(`Q1 actual ${status} response body did not arrive`));
    }, 15000);
    const check = () => {
      const request = q1Requests.slice(firstIndex).find(request => request.response?.status === status && (request.body !== undefined || request.captureError));
      if (!request) return;
      clearTimeout(timer); q1Waiters.delete(check);
      if (request.captureError) reject(new Error(request.captureError));
      else resolve(request);
    };
    q1Waiters.add(check); check();
  });
  const q1Snapshot = async stage => {
    const snapshot = await evaluate(page.send, `(() => {
      const state = window.fileLinksFixture.state();
      const source = document.querySelector('[data-file-links-source]');
      const pane = state.panes.find(p => p.type === 'doc' && p.params.path === ${JSON.stringify(recoveredPath)});
      const viewer = pane && document.querySelector('[data-file-links-document="' + pane.id + '"]');
      const anchor = document.querySelector(${JSON.stringify(missingSelector)});
      return { state, source: state.panes.find(p => p.id === source.dataset.fileLinksSource),
        sourceSameDOM: source === window.fileLinksQ1Source, document: pane,
        viewerSameDOM: !!viewer && viewer === window.fileLinksQ1Viewer,
        viewport: { scrollY, width: innerWidth, height: innerHeight },
        viewerText: viewer?.textContent, anchor: { href: anchor.getAttribute('href'), text: anchor.textContent, html: anchor.outerHTML },
        draft: { editor: source.querySelector('.ProseMirror').textContent,
          stored: JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)})) },
        scroll: ${scrollExpression}, clicks: window.fileLinksQ1Clicks };
    })()`);
    writeFileSync(path.join(artifacts, `q1-${stage}.json`), JSON.stringify(snapshot, null, 2));
    writeFileSync(path.join(artifacts, `q1-${stage}.html`), await evaluate(page.send, "document.documentElement.outerHTML"));
    return snapshot;
  };
  const q1Before = await q1Snapshot("before");
  assert.deepEqual(q1Before.draft, draftBefore, "Q1 starts with the inherited real editor and stored draft");
  assert.equal(q1Before.source.id, originalSource.id);
  const clickedURL = new URL(q1Before.anchor.href, url.origin);
  assert.equal(clickedURL.pathname, "/doc/file");
  assert.equal(clickedURL.searchParams.get("session"), process.env.DOCUMENT_FILE_LINKS_REF);
  assert.equal(clickedURL.searchParams.get("path"), recoveredTarget);
  const missingResponsePromise = q1Response(404, q1Requests.length);
  await click(page.send, missingSelector);
  const missingResponse = await missingResponsePromise;
  writeFileSync(path.join(artifacts, "q1-response-404.txt"), missingResponse.body);
  assert.equal(missingResponse.method, "GET");
  assert.equal(new URL(missingResponse.url).searchParams.get("format"), "raw");
  assert.equal(new URL(missingResponse.url).searchParams.get("session"), process.env.DOCUMENT_FILE_LINKS_REF);
  assert.equal(missingResponse.response.status, 404);
  assert(missingResponse.credentials?.cookieHeaderPresent, "production missing request carries real authentication cookie");
  assert(missingResponse.body.length > 0, "retain actual production missing response body");
  await until(page.send, `(() => {
    const state = window.fileLinksFixture.state();
    const pane = state.panes.find(p => p.type === 'doc' && p.params.path === ${JSON.stringify(recoveredPath)});
    const text = pane && document.querySelector('[data-file-links-document="' + pane.id + '"]')?.textContent;
    return state.focusedPaneId === pane?.id && text?.includes('File not available') && text.includes("This file was not found in the session's working directory.");
  })()`);
  await evaluate(page.send, `(() => {
    const pane = window.fileLinksFixture.state().panes.find(p => p.type === 'doc' && p.params.path === ${JSON.stringify(recoveredPath)});
    window.fileLinksQ1Viewer = document.querySelector('[data-file-links-document="' + pane.id + '"]');
    return true;
  })()`);
  const q1Missing = await q1Snapshot("missing");
  assert(q1BrowserLogs.some(entry => entry.source === "network" && entry.level === "error" &&
    entry.url === missingResponse.url && entry.text.includes("404")), "Chrome reports the intentional production404 diagnostic");
  const q1Identity = snapshot => ({ id: snapshot.document.id, params: snapshot.document.params,
    reference: snapshot.document.document.reference, origin: snapshot.document.document.origin });
  const assertQ1Preserved = snapshot => {
    assert.deepEqual(snapshot.source, q1Before.source, "Q1 retains exact source pane record");
    assert(snapshot.sourceSameDOM, "Q1 retains source DOM");
    assert(snapshot.viewerSameDOM, "Q1 keeps the same viewer mounted");
    assert.deepEqual(snapshot.draft, q1Before.draft, "Q1 preserves real editor and stored draft exactly");
    assert.equal(snapshot.state.focusedPaneId, q1Missing.document.id, "Q1 keeps selected document focused");
    assert.deepEqual(q1Identity(snapshot), q1Identity(q1Missing), "Q1 retains immutable owning document identity, not read generation");
    assert(Math.abs(snapshot.scroll.top - q1Before.scroll.top) <= 2, "Q1 does not move source transcript scroll");
  };
  assert.deepEqual(q1Missing.document.params, { session: process.env.DOCUMENT_FILE_LINKS_REF, path: recoveredPath, kind: "file" });
  assert.deepEqual(q1Missing.document.document.reference, { path: recoveredPath, cwd: process.env.DOCUMENT_FILE_LINKS_CWD,
    readTarget: recoveredTarget, provenance: "relative" });
  assert.deepEqual(q1Missing.document.document.origin, q1Before.source);
  assert.equal(q1Missing.clicks.length, 1);
  assert(q1Missing.clicks[0].trusted && q1Missing.clicks[0].button === 0);
  assert.equal(q1Missing.clicks[0].href, q1Before.anchor.href);
  assertQ1Preserved(q1Missing);
  // Pacing is the behavior under test here. Observe real frames for longer
  // than the first four transient retry intervals combined, without fake time.
  const terminalRequests = q1Requests.length;
  const terminalWindow = await evaluate(page.send, `(async () => {
    const start = performance.now();
    while (performance.now() - start < 16000) await new Promise(resolve => requestAnimationFrame(resolve));
    return { elapsedMs: performance.now() - start };
  })()`);
  const q1Terminal = await q1Snapshot("terminal");
  writeFileSync(path.join(artifacts, "q1-terminal-window.json"), JSON.stringify({ ...terminalWindow,
    requestsBefore: terminalRequests, requestsAfter: q1Requests.length }, null, 2));
  assert.equal(q1Requests.length, terminalRequests, "typed terminal 404 does not become a periodic retry");
  assertQ1Preserved(q1Terminal);
  const creation = await evaluate(page.send, `fetch('/doc/fixture/create-missing', { method: 'POST' }).then(async response =>
    ({ status: response.status, body: await response.text() }))`);
  const createdBytes = readFileSync(recoveredTarget);
  writeFileSync(path.join(artifacts, "q1-created-file.md"), createdBytes);
  writeFileSync(path.join(artifacts, "q1-create.json"), JSON.stringify({ ...creation,
    path: recoveredTarget, bytes: createdBytes.length, contents: createdBytes.toString("utf8") }, null, 2));
  assert.equal(creation.status, 204);
  assert.equal(creation.body, "");
  assert.deepEqual(createdBytes, Buffer.from(recoveredContents));
  const q1BeforeReload = await q1Snapshot("before-reload");
  assertQ1Preserved(q1BeforeReload);
  assert(q1BeforeReload.viewerText.includes('File not available'), "file creation alone does not conceal Reload with automatic recovery");
  assert.equal(q1Requests.length, terminalRequests);
  const q1ReloadTarget = await evaluate(page.send, `(() => {
    const viewer = window.fileLinksQ1Viewer;
    const button = [...viewer.querySelectorAll('button')].find(button => button.textContent.trim() === 'Reload');
    if (!button) throw new Error('Q1 actual visible Reload missing');
    const peers = [...button.parentElement.children].filter(el => el.tagName === 'BUTTON');
    const rect = button.getBoundingClientRect();
    const x = rect.left + rect.width / 2; const y = rect.top + rect.height / 2;
    return { selector: '[data-file-links-document="' + viewer.dataset.fileLinksDocument + '"] button:nth-of-type(' + (peers.indexOf(button) + 1) + ')',
      x, y, width: rect.width, height: rect.height, inViewport: rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight,
      hit: button.contains(document.elementFromPoint(x, y)), html: button.outerHTML };
  })()`);
  writeFileSync(path.join(artifacts, "q1-reload-target.json"), JSON.stringify(q1ReloadTarget, null, 2));
  assert(q1ReloadTarget.inViewport && q1ReloadTarget.hit && q1ReloadTarget.width > 0 && q1ReloadTarget.height > 0,
    "Q1 actual Reload is visible and hit-testable before trusted input");
  const reloadResponsePromise = q1Response(200, q1Requests.length);
  await click(page.send, q1ReloadTarget.selector);
  const reloadResponse = await reloadResponsePromise;
  writeFileSync(path.join(artifacts, "q1-response-200.md"), reloadResponse.body);
  assert.notEqual(reloadResponse.requestId, missingResponse.requestId, "actual Reload issues a fresh production request");
  assert.equal(reloadResponse.url, missingResponse.url, "Reload reads the captured source target");
  assert(reloadResponse.credentials?.cookieHeaderPresent, "Reload uses production cookie authentication");
  assert.equal(reloadResponse.response.status, 200);
  assert.equal(reloadResponse.body, recoveredContents);
  assert(!reloadResponse.response.fromDiskCache && !reloadResponse.response.fromServiceWorker, "new file bytes are not supplied by a browser response cache");
  await until(page.send, `window.fileLinksQ1Viewer?.textContent.includes('Q1 RECOVERED CURRENT FILE') &&
    !window.fileLinksQ1Viewer.textContent.includes('File not available')`);
  const q1After = await q1Snapshot("after");
  assertQ1Preserved(q1After);
  assert.equal(q1After.clicks.length, 2);
  assert(q1After.clicks[1].trusted && q1After.clicks[1].button === 0);
  assert.equal(q1After.clicks[1].text.trim(), "Reload");
  assert.equal(q1Requests.length, terminalRequests + 1, "only actual Reload adds a read after terminal missing");
  assert.equal(await evaluate(page.send, `window.fileLinksQ1Viewer.querySelector('p')?.textContent`), "Q1 RECOVERED CURRENT FILE", "useful production Markdown bytes render in mounted viewer");
  writeFileSync(path.join(artifacts, "q1-reload.json"), JSON.stringify({ activation: q1After.clicks[1],
    response: reloadResponse, beforeIdentity: q1Identity(q1BeforeReload), afterIdentity: q1Identity(q1After),
    beforeDraft: q1BeforeReload.draft, afterDraft: q1After.draft }, null, 2));
  page.ws.removeEventListener("message", q1NetworkListener);
  cases.push("Q1 real authenticated missing404, fixed filesystem create, trusted mounted Reload200 exact current bytes, same source/document identity and draft");
  // Independent direct journeys, no viewport change or fixture action between
  // trusted filename Open and the product's trusted top-bar Back.
  for (const journey of [
    { name: "primary-return", child: false, ref: process.env.DOCUMENT_FILE_LINKS_REF,
      cwd: process.env.DOCUMENT_FILE_LINKS_CWD, bytes: "PRIMARY CURRENT CWD FILE\n",
      draft: "Primary journey unsent editor input" },
    { name: "delegate-return", child: true, ref: process.env.DOCUMENT_FILE_LINKS_CHILD_REF,
      cwd: process.env.DOCUMENT_FILE_LINKS_CHILD_CWD, bytes: "DELEGATE CURRENT CWD FILE\n",
      draft: "Delegate journey parent unsent editor input" },
  ]) {
    const save = (name, value) => writeFileSync(path.join(artifacts, `${journey.name}-${name}.json`), JSON.stringify(value, null, 2));
    const relativePath = "docs/joined-return.md";
    const target = `${journey.cwd}/${relativePath}`;
    assert(journey.ref && journey.cwd, "fixture supplies independent owning ref and cwd");
    writeFileSync(path.join(artifacts, `${journey.name}-expected.md`), journey.bytes);
    assert.equal(readFileSync(target, "utf8"), journey.bytes, "independent literal current-cwd file control");
    await applyViewport(page.send, { width: 390, height: 844 });
    const parentID = await evaluate(page.send, "window.fileLinksFixture.prepareSource()");
    const parentSelector = `[data-file-links-source="${parentID}"]`;
    await until(page.send, `!!document.querySelector('${parentSelector} .ProseMirror[contenteditable="true"]')`);
    await click(page.send, `${parentSelector} .ProseMirror[contenteditable="true"]`);
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "a", code: "KeyA", windowsVirtualKeyCode: 65, modifiers: 2 });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "a", code: "KeyA", windowsVirtualKeyCode: 65, modifiers: 2 });
    await page.send("Input.insertText", { text: journey.draft });
    await until(page.send, `JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)}))?.text === ${JSON.stringify(journey.draft)}`);
    const parentDraft = await evaluate(page.send, `({ editor: document.querySelector('${parentSelector} .ProseMirror').textContent,
      stored: JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)})) })`);
    save("parent-input", parentDraft);
    assert.equal(parentDraft.editor, journey.draft, "real writable parent's editor contains actual unsent input");
    const sourceID = journey.child
      ? await evaluate(page.send, `window.fileLinksFixture.prepareSource(${JSON.stringify(journey.ref)})`)
      : parentID;
    const sourceSelector = `[data-file-links-source="${sourceID}"]`;
    const anchorSelector = `${sourceSelector} a[href$="joined-return.md"]`;
    await until(page.send, `!!document.querySelector(${JSON.stringify(anchorSelector)})`);
    // Observe trusted receipts without substituting input or product behavior.
    await evaluate(page.send, `(() => {
      window.fileLinksJourneyReceipts = [];
      if (window.fileLinksJourneyObserverInstalled) return true;
      window.fileLinksJourneyObserverInstalled = true;
      for (const type of ['click', 'wheel']) document.addEventListener(type, event => {
        const target = event.target.closest?.('a,button') ?? event.target;
        window.fileLinksJourneyReceipts.push({ type, trusted: event.isTrusted,
          button: event.button, deltaY: event.deltaY,
          target: type === 'click' ? target.outerHTML : target.className,
          text: type === 'click' ? target.textContent : undefined, at: performance.now() });
      }, { capture: true, once: false });
      return true;
    })()`);
    const sample = `(() => {
      const source = document.querySelector(${JSON.stringify(sourceSelector)});
      const anchor = document.querySelector(${JSON.stringify(anchorSelector)});
      if (!source || !anchor) return null;
      let port = anchor.parentElement;
      while (port && port !== source && !(['auto', 'scroll'].includes(getComputedStyle(port).overflowY) && port.scrollHeight > port.clientHeight + 100)) port = port.parentElement;
      if (!port || port === source) throw new Error('useful real source scroll port missing');
      const r = port.getBoundingClientRect(); const a = anchor.getClientRects()[0];
      const x = a.left + a.width / 2; const y = a.top + a.height / 2;
      return { top: port.scrollTop, height: port.scrollHeight, client: port.clientHeight,
        port: { tag: port.tagName, className: port.className, role: port.getAttribute('role'),
          sourcePaneId: source.dataset.fileLinksSource, overflowY: getComputedStyle(port).overflowY },
        rect: { left: r.left, top: r.top, width: r.width, height: r.height, right: r.right, bottom: r.bottom },
        usefulText: port.textContent.slice(0, 200), composerCount: source.querySelectorAll('.ProseMirror[contenteditable="true"]').length,
        anchor: { href: anchor.getAttribute('href'), text: anchor.textContent, html: anchor.outerHTML,
          rect: { left: a.left, right: a.right, top: a.top, bottom: a.bottom },
          x, y, visible: a.top >= r.top && a.bottom <= r.bottom && a.left >= 0 && a.right <= innerWidth &&
            anchor.contains(document.elementFromPoint(x, y)) } };
    })()`;
    const started = await evaluate(page.send, sample);
    save("start-port", started);
    let positioned = started;
    for (let n = 0; n < 80 && !positioned.anchor.visible; n++) {
      const deltaY = Math.max(-200, Math.min(200, positioned.anchor.y - (positioned.rect.top + positioned.rect.height / 2)));
      await page.send("Input.dispatchMouseEvent", { type: "mouseWheel",
        x: positioned.rect.left + positioned.rect.width / 2, y: positioned.rect.top + positioned.rect.height / 2,
        deltaX: 0, deltaY });
      positioned = await until(page.send, `(() => { const value = ${sample}; return value && value.top !== ${positioned.top} && value; })()`);
    }
    // Fresh stable readings, never wait for the desired saved offset.
    const stableSample = async () => evaluate(page.send, `(async () => {
      let prior;
      const deadline = performance.now() + 15000;
      for (;;) {
        await new Promise(resolve => requestAnimationFrame(resolve));
        const value = ${sample};
        if (value && prior && value.top === prior.top && value.height === prior.height && value.client === prior.client) return value;
        if (performance.now() > deadline) throw new Error('source geometry did not settle');
        prior = value;
      }
    })()`);
    const snapshot = async stage => {
      const value = await evaluate(page.send, `(() => {
        const state = window.fileLinksFixture.state();
        const parent = state.panes.find(pane => pane.id === ${JSON.stringify(parentID)});
        const source = state.panes.find(pane => pane.id === ${JSON.stringify(sourceID)});
        const doc = state.panes.find(pane => pane.type === 'doc' && pane.params.session === ${JSON.stringify(journey.ref)} && pane.params.path === ${JSON.stringify(relativePath)});
        return { state, parent, source, doc, position: ${sample},
          viewport: { width: innerWidth, height: innerHeight, scrollY }, pathname: location.pathname,
          draft: { editor: document.querySelector('${parentSelector} .ProseMirror')?.textContent ?? null,
            stored: JSON.parse(localStorage.getItem('evener.composer.draft.v2.' + ${JSON.stringify(process.env.DOCUMENT_FILE_LINKS_REF)})) },
          viewerText: doc && document.querySelector('[data-file-links-document="' + doc.id + '"]')?.textContent,
          receipts: window.fileLinksJourneyReceipts };
      })()`);
      save(stage, value);
      writeFileSync(path.join(artifacts, `${journey.name}-${stage}.html`), await evaluate(page.send, "document.documentElement.outerHTML"));
      const screenshot = await page.send("Page.captureScreenshot");
      writeFileSync(path.join(artifacts, `${journey.name}-${stage}.png`), Buffer.from(screenshot.result.data, "base64"));
      return value;
    };
    await stableSample();
    const before = await snapshot("before");
    assert(before.position.anchor.visible, "actual assistant filename visible and hit-testable before input");
    assert(before.position.top > 100 && before.position.height - before.position.client - before.position.top > 100,
      "source reading is away from both ends before Open");
    assert.notEqual(before.position.top, started.top, "trusted wheel actually moved source reading");
    assert(before.receipts.some(event => event.type === "wheel" && event.trusted), "trusted wheel receipt");
    assert(before.position.rect.width > 300 && before.position.rect.height > 300 && before.position.usefulText.length > 100,
      "source has useful visible real content and scroll geometry");
    assert.deepEqual(before.source.params, journey.child ? { ref: journey.ref, parentRef: process.env.DOCUMENT_FILE_LINKS_REF } : { ref: journey.ref });
    assert.equal(before.source.type, journey.child ? "transcript" : "session");
    assert.equal(before.source.slot, journey.child ? "secondary" : "main");
    assert.equal(before.position.composerCount, journey.child ? 0 : 1);
    assert.equal(before.draft.stored.text, journey.draft);
    assert.deepEqual(before.draft.stored, parentDraft.stored);
    if (!journey.child) assert.equal(before.draft.editor, journey.draft);
    const anchorURL = new URL(before.position.anchor.href, url.origin);
    assert.equal(anchorURL.searchParams.get("session"), journey.ref);
    assert.equal(anchorURL.searchParams.get("path"), target);
    const network = [];
    let completeResponse;
    let rejectResponse;
    const responsePromise = new Promise((resolve, reject) => { completeResponse = resolve; rejectResponse = reject; });
    const listener = event => {
      const frame = JSON.parse(event.data); const p = frame.params;
      if (!p) return;
      if (frame.method === "Network.requestWillBeSent") {
        const requested = new URL(p.request.url);
        if (requested.pathname === "/doc/file" && requested.searchParams.get("session") === journey.ref && requested.searchParams.get("path") === target)
          network.push({ requestId: p.requestId, url: p.request.url, method: p.request.method, initiator: p.initiator.type });
      }
      const request = network.find(request => request.requestId === p.requestId);
      if (!request) return;
      if (frame.method === "Network.responseReceived") request.response = p.response;
      if (frame.method === "Network.loadingFinished") page.send("Network.getResponseBody", { requestId: p.requestId }).then(frame => {
        request.body = frame.result.base64Encoded ? Buffer.from(frame.result.body, "base64").toString("utf8") : frame.result.body;
        save("network", network);
        writeFileSync(path.join(artifacts, `${journey.name}-response.md`), request.body);
        completeResponse(request);
      }).catch(rejectResponse);
      save("network", network);
    };
    page.ws.addEventListener("message", listener);
    await click(page.send, anchorSelector);
    const response = await responsePromise;
    await until(page.send, `document.querySelector('[data-file-links-document="' + window.fileLinksFixture.state().focusedPaneId + '"]')?.textContent.includes(${JSON.stringify(journey.bytes.trim())})`);
    const opened = await snapshot("open");
    assert.equal(response.method, "GET");
    assert.equal(new URL(response.url).searchParams.get("format"), "raw");
    assert.equal(response.response.status, 200);
    assert.equal(response.body, journey.bytes, "actual owning cwd HTTP bytes, not parent's reused response");
    assert.equal(opened.state.focusedPaneId, opened.doc.id);
    assert.deepEqual(opened.doc.params, { session: journey.ref, path: relativePath, kind: "file" });
    assert.deepEqual(opened.doc.document.reference, { path: relativePath, cwd: journey.cwd, readTarget: target, provenance: "relative" });
    assert.deepEqual(opened.source, { ...before.source, slot: "main" });
    assert.deepEqual(opened.doc.document.origin, opened.source);
    assert.deepEqual(opened.parent, { ...before.parent, slot: journey.child ? "secondary" : "main" });
    assert.deepEqual(opened.draft.stored, before.draft.stored);
    const backTarget = await evaluate(page.send, `(() => {
      const button = document.querySelector('[aria-label="Back"]'); const r = button?.getBoundingClientRect();
      return r && { html: button.outerHTML, x: r.left + r.width / 2, y: r.top + r.height / 2,
        visible: r.width > 0 && r.height > 0 && r.top >= 0 && r.bottom <= innerHeight &&
          button.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)) };
    })()`);
    save("back-target", backTarget);
    assert(backTarget?.visible, "actual product Back control visible and hit-testable");
    await click(page.send, '[aria-label="Back"]');
    await until(page.send, `window.fileLinksFixture.state().focusedPaneId === ${JSON.stringify(sourceID)} && !!document.querySelector(${JSON.stringify(anchorSelector)})`);
    await stableSample();
    const returned = await snapshot("back");
    page.ws.removeEventListener("message", listener);
    assert.deepEqual(returned.viewport, before.viewport, "same viewport throughout direct journey");
    assert.deepEqual(opened.viewport, before.viewport);
    assert.deepEqual(returned.source, { ...before.source, slot: "main" }, "exact source pane id, type and ref returned");
    assert.deepEqual(returned.position.port, before.position.port, "same real source scroll port identity");
    assert(Math.abs(returned.position.top - before.position.top) <= 2, "fresh independently sampled direct Back reading position within2px");
    assert(returned.position.top > 100 && returned.position.height - returned.position.client - returned.position.top > 100);
    assert.deepEqual(returned.draft.stored, before.draft.stored, "parent unsent draft retained exactly");
    assert.deepEqual(returned.parent, opened.parent, "original parent retained after child promotion and Back");
    if (!journey.child) assert.equal(returned.draft.editor, journey.draft);
    const clicks = returned.receipts.filter(event => event.type === "click");
    assert.equal(clicks.length, 2);
    assert(clicks.every(event => event.trusted && event.button === 0));
    assert(clicks[0].target.includes("joined-return.md") && clicks[1].target.includes("Back"), "trusted filename and product Back receipts");
    save("result", { assertions: "pass", before, opened, returned, response, deltaPx: returned.position.top - before.position.top });
    cases.push(`${journey.name}, trusted assistant filename Open, owning current-cwd HTTP200 literal bytes, actual Back, same-viewport away position within2px and parent draft`);
  }
  writeFileSync(path.join(artifacts, "result.json"), JSON.stringify({ assertions: "pass", cases }, null, 2));
} catch (error) {
  if (page) writeFileSync(path.join(artifacts, "route-after.json"), JSON.stringify(await evaluate(page.send, 'window.fileLinksFixture.route()'), null, 2));
  if (page) writeFileSync(path.join(artifacts, "document.html"), await evaluate(page.send, "document.documentElement.outerHTML"));
  if (page) writeFileSync(path.join(artifacts, "state.json"), JSON.stringify(await evaluate(page.send, "(async () => ({ state: window.fileLinksFixture.state(), clicks: window.fileLinksClickEvidence, requests: await window.fileLinksFixture.requests(), viewers: [...document.querySelectorAll('[data-file-links-document]')].map(el => ({ id: el.dataset.fileLinksDocument, text: el.textContent })) }))()"), null, 2));
  writeFileSync(path.join(artifacts, "failure.txt"), error.stack);
  // Bounded post-failure observation, never replaces or catches the original
  // failed oracle. A source-bound explicit reopen has its own focus contract,
  // distinct from generic direct-open route-precedence controls.
  if (page && await evaluate(page.send, "window.fileLinksFixture.state().panes.some(p => p.type === 'doc' && p.params.path.endsWith('design.md')) && window.fileLinksClickEvidence?.length === 1")) {
    const beforeReopen = await evaluate(page.send, "({ state: window.fileLinksFixture.state(), route: window.fileLinksFixture.route() })");
    await click(page.send, '[data-file-links-source] a[href$="design.md"]');
    const prior = beforeReopen.state.panes.find(p => p.type === 'doc');
    await until(page.send, `window.fileLinksFixture.state().panes.find(p => p.id === ${JSON.stringify(prior.id)})?.document.reopen === ${prior.document.reopen + 1}`);
    const afterReopen = await evaluate(page.send, `(async () => {
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      return { state: window.fileLinksFixture.state(), route: window.fileLinksFixture.route(),
        clicks: window.fileLinksClickEvidence, requests: await window.fileLinksFixture.requests(),
        viewers: [...document.querySelectorAll('[data-file-links-document]')].map(el => ({ id: el.dataset.fileLinksDocument, text: el.textContent })) };
    })()`);
    writeFileSync(path.join(artifacts, 'explicit-reopen.json'), JSON.stringify({ beforeReopen, afterReopen }, null, 2));
    writeFileSync(path.join(artifacts, 'explicit-reopen.html'), await evaluate(page.send, 'document.documentElement.outerHTML'));
  }
  throw error;
} finally {
  page?.close();
  await guard?.cleanup();
}
