import { useState } from "react";
import { type Insight, InsightCard } from "../../widgets/insightcard";
import { ThemeFlip } from "../ThemeFlip";

// Plausible agent-usage insights, plural on purpose so the pager has
// somewhere real to go - not a repeated placeholder.
const INSIGHTS: Insight[] = [
  {
    title: "Token spend is climbing",
    body: "This week's token usage is up 18% over last week, driven mostly by longer transcripts in code-review sessions.",
    series: [420, 460, 455, 510, 540, 590, 610],
  },
  {
    title: "Three sessions waiting on you",
    body: "Idle-session-1, Idle-session-2, and a spawn preview have been sitting in needs-you for over an hour.",
  },
  {
    title: "Retry rate improved",
    body: "Tool-call retries dropped after yesterday's model-catalog update - down from roughly 1 in 12 calls to 1 in 40.",
    series: [8.4, 7.9, 6.1, 5.8, 4.2, 2.5],
  },
];

function LiveInsightCard() {
  const [page, setPage] = useState(0);
  return <InsightCard insights={INSIGHTS} page={page} onPageChange={setPage} />;
}

export default function InsightCardGallerySection() {
  return (
    <section>
      <h2>InsightCard</h2>
      <ThemeFlip>
        <LiveInsightCard />
      </ThemeFlip>
    </section>
  );
}
