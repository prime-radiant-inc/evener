import { useMemo, useState } from "react";
import { Table, type TableColumn, type TableFilter, type TableSortDir } from "../../widgets/table";
import { ThemeFlip } from "../ThemeFlip";

interface ModelRow {
  id: string;
  name: string;
  provider: string;
  tags: string[];
  status: "active" | "retired";
}

// Mixed providers among the ACTIVE rows on purpose: the default filter is
// "Active" (see LiveModelsTable below), so a fixture where every active row
// shared one provider made sorting the Provider column a no-op until you
// also flipped to "All" - nothing to see, nothing proving the sort worked.
const ROWS: ModelRow[] = [
  { id: "1", name: "claude-opus-4.6", provider: "Anthropic", tags: ["reasoning", "long-context"], status: "active" },
  { id: "2", name: "claude-sonnet-4.6", provider: "Anthropic", tags: ["balanced"], status: "active" },
  { id: "3", name: "claude-haiku-3.5", provider: "Anthropic", tags: ["fast", "cheap"], status: "active" },
  { id: "4", name: "gpt-5", provider: "OpenAI", tags: ["reasoning"], status: "active" },
  { id: "5", name: "gemini-2.5-pro", provider: "Google", tags: ["long-context"], status: "active" },
  { id: "6", name: "gpt-4-legacy", provider: "OpenAI", tags: ["deprecated"], status: "retired" },
];

const COLUMNS: TableColumn<ModelRow>[] = [
  { key: "name", label: "Model", sortable: true },
  { key: "provider", label: "Provider", sortable: true },
  {
    key: "tags",
    label: "Tags",
    render: (row) => row.tags.join(", "),
  },
];

function LiveModelsTable() {
  const [sortKey, setSortKey] = useState("name");
  const [sortDir, setSortDir] = useState<TableSortDir>("ascending");
  const [statusFilter, setStatusFilter] = useState<"active" | "all">("active");

  const filters: TableFilter[] = [
    { key: "active", label: "Active", active: statusFilter === "active" },
    { key: "all", label: "All", active: statusFilter === "all" },
  ];

  const visibleRows = useMemo(() => {
    const filtered = statusFilter === "active" ? ROWS.filter((row) => row.status === "active") : ROWS;
    const sorted = [...filtered].sort((a, b) => {
      const value = sortKey === "name" ? a.name.localeCompare(b.name) : a.provider.localeCompare(b.provider);
      return sortDir === "ascending" ? value : -value;
    });
    return sorted;
  }, [sortKey, sortDir, statusFilter]);

  const toggleSort = (key: string) => {
    if (key === sortKey) {
      setSortDir((dir) => (dir === "ascending" ? "descending" : "ascending"));
    } else {
      setSortKey(key);
      setSortDir("ascending");
    }
  };

  return (
    <Table
      columns={COLUMNS}
      rows={visibleRows}
      rowKey={(row) => row.id}
      sortKey={sortKey}
      sortDir={sortDir}
      onSortChange={toggleSort}
      filters={filters}
      onFilterToggle={(key) => setStatusFilter(key as "active" | "all")}
      empty={<span>No models match this filter.</span>}
    />
  );
}

export default function TableGallerySection() {
  return (
    <section>
      <h2>Table</h2>
      <ThemeFlip>
        <LiveModelsTable />
      </ThemeFlip>
    </section>
  );
}
