import { PropertyList } from "../../widgets/propertylist";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./propertylist.module.css";

export default function PropertyListGallerySection() {
  return (
    <section>
      <h2>PropertyList</h2>
      <ThemeFlip>
        <div className={styles.sample}>
          <PropertyList
            rows={[
              { label: "Version", value: "v1.2.0", mono: true },
              { label: "Marketplace", value: "evener-official" },
              {
                label: "Install location",
                value: "~/.local/share/evener/plugins/marketplaces/evener-official/superpowers",
                mono: true,
              },
              { label: "Last updated", value: "3 days ago" },
            ]}
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
