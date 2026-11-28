import { useState } from "react";
import { type TabOption, Tabs } from "../../widgets/tabs";
import { GallerySection } from "../GallerySection";

const OVERVIEW_OPTIONS: readonly TabOption[] = [
  { value: "about", label: "About" },
  { value: "links", label: "Links", detail: "12", accessibleLabel: "Links, 12" },
  { value: "agents", label: "Agents", detail: "0/52", accessibleLabel: "Agents, 0 of 52 active" },
  { value: "jobs", label: "Jobs", detail: "2/397", accessibleLabel: "Jobs, 2 of 397 running" },
  { value: "watches", label: "Watches", detail: "—", accessibleLabel: "Watches, counts unknown" },
  { value: "tasks", label: "Tasks", detail: "27/30", accessibleLabel: "Tasks, 27 of 30 settled" },
];

const SIMPLE_OPTIONS: readonly TabOption[] = [
  { value: "details", label: "Details" },
  { value: "history", label: "History" },
  { value: "raw", label: "Raw" },
];

function InteractiveOverviewTabs() {
  const [value, setValue] = useState("about");
  return <Tabs label="Overview kind" value={value} options={OVERVIEW_OPTIONS} onChange={setValue} />;
}

function InteractiveSimpleTabs() {
  const [value, setValue] = useState("details");
  return <Tabs label="Inspector view" value={value} options={SIMPLE_OPTIONS} onChange={setValue} />;
}

export default function TabsSection() {
  return (
    <GallerySection
      title="Tabs"
      note="Flush underline tab strip with optional two-level count details, roving Arrow/Home/End navigation, and an inset square :focus-visible ring."
    >
      <InteractiveOverviewTabs />
      <InteractiveSimpleTabs />
    </GallerySection>
  );
}
