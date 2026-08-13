import { useState } from "react";
import { Button } from "../../widgets/button";
import { DiffTable, type DiffTableColumn, type DiffTableRow } from "../../widgets/difftable";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./difftable.module.css";

const COLUMNS: DiffTableColumn[] = [
  { key: "flavor", label: "Flavor" },
  { key: "category", label: "Category" },
  { key: "supplier", label: "Supplier" },
];

const PLAIN_ROWS: DiffTableRow[] = [
  {
    key: "1",
    cells: { flavor: { value: "Vanilla" }, category: { value: "Classic" }, supplier: { value: "maple-orbit" } },
  },
  { key: "2", cells: { flavor: { value: "Maple" }, category: { value: "Retro" }, supplier: { value: "maple-orbit" } } },
  {
    key: "3",
    cells: { flavor: { value: "Pumpkin" }, category: { value: "Seasonal" }, supplier: { value: "harvest-co" } },
  },
];

const PROPOSED_ROWS: DiffTableRow[] = [
  {
    key: "1",
    cells: { flavor: { value: "Vanilla" }, category: { value: "Classic" }, supplier: { value: "maple-orbit" } },
  },
  {
    key: "2",
    cells: {
      flavor: { value: "Maple", proposed: "Maple Pecan" },
      category: { value: "Retro", proposed: "Classic" },
      supplier: { value: "maple-orbit" },
    },
  },
  {
    key: "3",
    cells: {
      flavor: { value: "Pumpkin" },
      category: { value: "Seasonal" },
      supplier: { value: "harvest-co", proposed: "harvest-collective" },
    },
  },
];

function LiveDiffTable() {
  const [proposed, setProposed] = useState(false);
  return (
    <div className={styles.stack}>
      <Button size="sm" onClick={() => setProposed((v) => !v)}>
        {proposed ? "Revert proposed edit" : "Propose menu cleanup"}
      </Button>
      <DiffTable columns={COLUMNS} rows={proposed ? PROPOSED_ROWS : PLAIN_ROWS} />
    </div>
  );
}

export default function DiffTableGallerySection() {
  return (
    <section>
      <h2>DiffTable</h2>
      <ThemeFlip>
        <LiveDiffTable />
      </ThemeFlip>
    </section>
  );
}
