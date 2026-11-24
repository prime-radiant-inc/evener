// Minimal CSS-Modules `composes:` resolver, plus `:global()` unwrapping.
//
// Vite/postcss-modules resolves `composes: X from "./other.module.css"` at
// BUILD time by merging classnames. A layoutguard case loads .module.css
// files directly as plain <link rel="stylesheet"> tags with NO build step,
// so `composes` (not a real CSS property) is silently dropped by the browser
// and the composed rules never apply. For widgets/internal/fieldTrigger
// .module.css (composed into the path-field and model-field triggers) that
// silently drops white-space:nowrap - which is exactly the property kata
// p6g8's bug and fix depend on. A harness that skips this resolution step
// tests nothing and will not react to the mutation it exists to catch.
//
// The same build-step gap applies to `:global(...)`: in production it means
// "don't hash this part" (used to pierce into a child component's own
// classes, e.g. spawn.module.css constraining ModelSwitchTrigger's nested
// spans). A browser parsing raw CSS treats `:global()` as an unknown
// pseudo-class and drops the WHOLE selector, so the rule silently never
// applies in the harness. Unwrapping `:global(<inner>)` to `<inner>`
// mirrors the build exactly for the simple element/class inners this
// codebase uses.
//
// For every simple `.class { ... }` rule across ALL provided sources, a
// `composes: x from "...";` line is replaced with the target rule's own
// declarations, and the target's own composes are resolved the same way: the
// build merges class names, so a chain of compositions is just more classes
// on the element. A cycle is refused rather than walked forever.
//
// The one refinement correctness demands: a composes target whose rule sits
// INSIDE a media block must not have its body spliced ungated. The build step
// composes by MERGING CLASSNAMES (postcss-modules): the consuming element
// keeps the composed class, and the gate stays wrapped around the target's
// own rule, applying to the element through that class. Splicing the gated
// body into the ungated consumer drops the gate in the harness - for the
// rail seat's gutter pull that would drag a glyph out of the pane at phone
// widths, the exact escape the rail cases exist to catch. So a gated
// target's declarations are re-stated AFTER the consumer's rule, inside the
// same gate, on the consumer's own selector (the harness markup names only
// the consumer class). One combination is refused instead of resolved: a
// gated consumer composing a gated target, whose correct form would have to
// keep the two gates independent rather than AND-ed; nothing in this
// codebase needs it, and guessing would silently measure the wrong geometry.

// A plain single-class selector immediately followed by its rule body - the
// only shape `composes` is allowed to target. Deliberately does not match
// compound selectors (.trigger:hover) or grouped ones (.a, .b): requiring
// the class name to be followed immediately by optional horizontal
// whitespace then `{` rules those out.
const RULE_RE = /(\.[A-Za-z0-9_-]+)[ \t]*\{([^}]*)\}/g;
const COMPOSES_RE = /composes:\s*([^;]+?)\s+from\s+["'][^"']*["'];?/g;

// `:global(<inner>)` unwrapping: the build strips the wrapper and keeps the
// inner selector verbatim. The pattern below only matches paren-free inners;
// anything nested (e.g. `:global(:not(.x))`) matches NOTHING, so the leftover
// check after the replace throws rather than letting the browser silently
// drop the selector and the guard pass without testing the rule.
const GLOBAL_RE = /:global\(\s*([^()]+?)\s*\)/g;
const LEFTOVER_GLOBAL_RE = /:global\(/;

function prefixSelectorList(selector, prefix) {
  let out = "";
  let i = 0;
  while (i < selector.length) {
    const ch = selector[i];
    if (ch === '"' || ch === "'") {
      const quote = ch;
      let end = i + 1;
      while (end < selector.length) {
        if (selector[end] === "\\") end += 2;
        else if (selector[end] === quote) {
          end += 1;
          break;
        } else end += 1;
      }
      out += selector.slice(i, end);
      i = end;
      continue;
    }
    // A :global(...) inner is not this module's class, so it must survive
    // unwrapGlobal unprefixed. Skip the whole balanced call.
    if (selector.startsWith(":global(", i)) {
      let depth = 1;
      let end = i + ":global(".length;
      while (end < selector.length && depth > 0) {
        if (selector[end] === "(") depth += 1;
        else if (selector[end] === ")") depth -= 1;
        end += 1;
      }
      out += selector.slice(i, end);
      i = end;
      continue;
    }
    if (ch === "." && /[-_a-zA-Z]/.test(selector[i + 1] ?? "")) {
      let end = i + 1;
      while (end < selector.length && /[-_a-zA-Z0-9]/.test(selector[end])) end += 1;
      out += `.${prefix}${selector.slice(i + 1, end)}`;
      i = end;
      continue;
    }
    out += ch;
    i += 1;
  }
  return out;
}

/**
 * Prefixes every class selector in one module's stylesheet, plus every
 * same-file `composes:` target, so two modules whose unhashed class names
 * collide can be loaded into one layoutguard sheet. A `composes: x from "..."`
 * target belongs to the other module and is left alone, and a `:global(...)`
 * inner is left for unwrapGlobal.
 */
export function prefixClassSelectors(cssText, prefix) {
  if (!/^[-_a-zA-Z][-_a-zA-Z0-9]*$/.test(prefix)) {
    throw new Error(`prefixClassSelectors: ${JSON.stringify(prefix)} is not a CSS identifier prefix`);
  }
  const css = cssText.replace(/\/\*[\s\S]*?\*\//g, "");
  let out = "";
  let start = 0;
  let quote = null;
  for (let i = 0; i < css.length; i++) {
    const ch = css[i];
    if (quote !== null) {
      if (ch === "\\") i += 1;
      else if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      continue;
    }
    if (ch === "{") {
      const prelude = css.slice(start, i);
      out += prelude.trimStart().startsWith("@") ? prelude : prefixSelectorList(prelude, prefix);
      out += "{";
      start = i + 1;
    } else if (ch === "}") {
      out += `${css.slice(start, i)}}`;
      start = i + 1;
    }
  }
  out += css.slice(start);
  return out.replace(/composes:\s*([^;{}]+);/g, (whole, names) => {
    if (/\bfrom\s+["']/.test(names)) return whole;
    const list = names
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean);
    if (list.length === 0) return whole;
    return `composes: ${list.map((name) => `${prefix}${name}`).join(", ")};`;
  });
}

export function unwrapGlobal(cssText) {
  const unwrapped = cssText.replace(GLOBAL_RE, (_whole, inner) => {
    if (!/^[a-zA-Z.*#:[\]]/.test(inner)) {
      throw new Error(`unwrapGlobal: unsupported :global() inner ${JSON.stringify(inner)} - extend the pattern`);
    }
    return inner;
  });
  if (LEFTOVER_GLOBAL_RE.test(unwrapped)) {
    throw new Error("unwrapGlobal: unmatched :global( remains (nested parens?) - extend the pattern");
  }
  return unwrapped;
}

export function resolveComposes(cssTexts) {
  // Comments are stripped FIRST, before any position scan: an "@media" or
  // ":global(" mentioned in comment prose would otherwise pair with the next
  // `{` (MEDIA_OPEN_RE below) or trip unwrapGlobal's leftover check, turning
  // comment text into a malformed gate or a spurious throw - real files in
  // this tree mention both (holdhints.module.css, welcome.module.css). The
  // resolved sheet loses its comments, which the browser ignores anyway.
  const combined = cssTexts
    .map((css) => css.replace(/\/\*[\s\S]*?\*\//g, ""))
    .map(unwrapGlobal)
    .join("\n\n");

  // Every @media block, brace-counted so nested rules cannot end one early:
  // the map from a rule's position to the gate it sits under.
  const mediaBlocks = [];
  const MEDIA_OPEN_RE = /@media[^{]*\{/g;
  let m = MEDIA_OPEN_RE.exec(combined);
  while (m !== null) {
    const open = m.index + m[0].length - 1;
    let depth = 0;
    let end = -1;
    for (let i = open; i < combined.length; i++) {
      if (combined[i] === "{") depth++;
      else if (combined[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end === -1) {
      throw new Error("resolveComposes: unterminated @media block");
    }
    mediaBlocks.push({ gate: m[0].replace(/\{\s*$/, "").trim(), start: m.index, end });
    MEDIA_OPEN_RE.lastIndex = end + 1;
    m = MEDIA_OPEN_RE.exec(combined);
  }

  const gateAt = (index) => {
    for (const block of mediaBlocks) {
      if (index > block.start && index < block.end) return block.gate;
    }
    return null;
  };

  // Every simple rule keyed by class name, KEEPING source order and every
  // definition: a class may be defined both ungated and inside a block, and
  // composing it must carry both (the same element carries both rules in the
  // built app).
  //
  // Each rule also carries its position as an identity: `composes: input from
  // "../input/input.module.css"` in a module that itself defines `.input`
  // names a DIFFERENT rule, so cycle detection must compare rules, not class
  // names.
  const ruleIdByOffset = new Map();
  const bodies = new Map();
  let nextRuleId = 0;
  for (const m of combined.matchAll(RULE_RE)) {
    const id = nextRuleId++;
    ruleIdByOffset.set(m.index, id);
    const entries = bodies.get(m[1]) ?? [];
    entries.push({ id, body: m[2], gate: gateAt(m.index) });
    bodies.set(m[1], entries);
  }

  // Resolves one rule body by replacing each composes line, IN PLACE, with the
  // target rules' own resolved bodies. The position matters: declarations the
  // consumer writes after `composes` come after the composed declarations and
  // win, exactly as in the built app. Appending composed declarations at the
  // end would silently reverse that cascade (directorypicker's `.input`
  // composes input.module.css's sans-serif font and then sets its own
  // monospace one, so appending measures the path field in the wrong font).
  //
  // Each composed class keeps its OWN media gate - the build merges class
  // names, so a gate belongs to the class whose rule carries it, never to the
  // consumer's chain - and gated declarations are re-stated after the
  // consumer's rule on the consumer's selector.
  const resolveBody = (body, consumerGate, stack, selfId, gated) => {
    const addGated = (gate, declarations) => {
      if (declarations === "") return;
      gated.set(gate, (gated.get(gate) ?? []).concat([declarations]));
    };

    let out = "";
    let last = 0;
    for (const match of body.matchAll(COMPOSES_RE)) {
      out += body.slice(last, match.index);
      last = match.index + match[0].length;
      const names = match[1];
      // One class per composes line. A comma list would not mean what it
      // looks like it means: the build (postcss-modules) binds `from` to the
      // last comma entry only, so every earlier name resolves as a LOCAL
      // class there - the vite build fails on the form rather than resolving
      // it, and this resolver refuses it for the same reason instead of
      // resolving a list the app can never ship.
      if (names.includes(",")) {
        throw new Error(
          `composes: "${names}" - the build binds \`from\` to the last comma entry only; compose one class per composes line instead`,
        );
      }
      const name = names.trim();
      const entries = bodies.get(`.${name}`);
      if (entries === undefined || entries.length === 0) {
        // A class that only appears inside compound or grouped selectors
        // (.a::after, .b > div > :last-child) styles pseudo-elements and
        // descendants, never the consumer's own box, so the consumer's
        // declarations have nothing to inherit from it; only a class defined
        // nowhere is an error.
        if (new RegExp(`\\.${name}(?![A-Za-z0-9_-])`).test(combined)) continue;
        throw new Error(
          `composes references .${name}, but no such rule was found in the provided CSS sources - add the module that defines it to the case's cssFiles`,
        );
      }
      for (const entry of entries) {
        // A rule composing a same-named class from another module names that
        // other rule; its own rule is not a cycle.
        if (entry.id === selfId) continue;
        if (entry.gate !== null && consumerGate !== null) {
          throw new Error(
            `composes in a rule gated by "${consumerGate}" references the media-gated .${name}: the composed class's gate must stay independent of the consumer's - extend resolveComposes instead`,
          );
        }
        if (stack.has(entry.id)) {
          throw new Error(`composes cycle through .${name}`);
        }
        const entryStack = new Set(stack).add(entry.id);
        const resolved = resolveBody(entry.body, entry.gate ?? consumerGate, entryStack, entry.id, gated);
        if (entry.gate === null) out += resolved;
        else addGated(entry.gate, resolved.trim());
      }
    }
    return out + body.slice(last);
  };

  return combined.replace(RULE_RE, (_whole, selector, body, offset) => {
    const own = body.replace(COMPOSES_RE, "").trim();
    // No composes: leave the rule text byte-identical. Cases assert on the
    // resolved sheet's exact whitespace in places, and rewriting every rule
    // would perturb sheets this resolver has no business touching.
    if (own === body.trim()) return _whole;
    const selfId = ruleIdByOffset.get(offset);
    const gated = new Map();
    const resolvedBody = resolveBody(body, gateAt(offset), new Set([selfId]), selfId, gated);
    let out = `${selector} {${resolvedBody}}`;
    for (const [gate, parts] of gated) {
      out += `\n\n${gate} {\n  ${selector} {\n${parts.map((d) => `    ${d}`).join("\n")}\n  }\n}`;
    }
    return out;
  });
}
