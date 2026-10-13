// dom-accessibility-api (Testing Library's own accname implementation, used by
// accessibleNameTestUtils.ts) ships its declarations at dist/index.d.ts but
// its package.json "exports" map names only the .mjs/.js entry points, so the
// "bundler" module resolution this project uses never finds them. This
// declares the one function the tests call, matching
// dist/accessible-name.d.ts. If the package adds a "types" export, delete this.
declare module "dom-accessibility-api" {
  export function computeAccessibleName(root: Element): string;
}
