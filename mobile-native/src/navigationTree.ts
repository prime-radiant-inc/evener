/** Project expanded destinations into one virtualized list, retaining server order. */
export function navigationTree<T>(
  roots: readonly T[],
  key: (item: T) => string,
  children: ((item: T) => readonly T[]) | undefined,
  expanded: ReadonlySet<string>,
) {
  const rows: { item: T; depth: number }[] = [];
  const seen = new Set<string>();
  function visit(items: readonly T[], depth: number) {
    for (const item of items) {
      const ref = key(item);
      if (seen.has(ref)) continue;
      seen.add(ref);
      rows.push({ item, depth });
      if (expanded.has(ref) && children) visit(children(item), depth + 1);
    }
  }
  visit(roots, 0);
  return rows;
}
