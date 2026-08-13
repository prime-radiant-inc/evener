import { RecommendationCard } from "../../widgets/recommendationcard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./recommendationcard.module.css";

export default function RecommendationCardGallerySection() {
  return (
    <section>
      <h2>RecommendationCard</h2>
      <ThemeFlip>
        <div className={styles.case}>
          <p className={styles.caseLabel}>full: confidence, alternatives, footer</p>
          <RecommendationCard
            title="Scale the eval runner pool to 6 workers"
            body="Queue depth has held above 40 jobs for the last 20 minutes and the p95 wait time is climbing. Adding workers should clear the backlog within a cycle."
            confidence={0.87}
            onAccept={() => {}}
            onReject={() => {}}
            alternatives={[
              { label: "Scale to 4 instead", onSelect: () => {} },
              { label: "Raise the queue timeout", onSelect: () => {} },
            ]}
          />
        </div>
        <div className={styles.case}>
          <p className={styles.caseLabel}>no confidence, no alternatives</p>
          <RecommendationCard
            title="Rotate the stale API key for job-store"
            body="This key was minted 400 days ago and has no expiry set. Rotating it now avoids an unplanned outage later."
            onAccept={() => {}}
            onReject={() => {}}
          />
        </div>
        <div className={styles.case}>
          <p className={styles.caseLabel}>passive (no footer)</p>
          <RecommendationCard
            title="Prefetch the model catalog on session start"
            body="Sessions that open the model picker within the first minute pay a cold-fetch penalty; prefetching removes it."
            confidence={0.62}
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
