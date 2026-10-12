import { PropertyList } from "../../widgets/propertylist";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./propertylist.module.css";

const INSTALL_PATH = "~/.local/share/evener/plugins/marketplaces/evener-official/superpowers";

export default function PropertyListGallerySection() {
  return (
    <section>
      <h2>PropertyList</h2>
      <ThemeFlip>
        <h3>inline (default): values truncate</h3>
        <div className={styles.sample}>
          <PropertyList
            rows={[
              { label: "Version", value: "v1.2.0", mono: true },
              { label: "Marketplace", value: "evener-official" },
              { label: "Install location", value: INSTALL_PATH, mono: true },
              { label: "Last updated", value: "3 days ago" },
            ]}
          />
        </div>
        <h3>inline, wrap rows: values being confirmed show in full</h3>
        <div className={styles.sample}>
          <PropertyList
            rows={[
              { label: "Host", value: "build-box" },
              { label: "Target", value: "evener@build-box.tailnet.example:22", mono: true, wrap: true },
              { label: "Install path", value: INSTALL_PATH, mono: true, wrap: true },
            ]}
          />
        </div>
        <h3>stacked: label over value, optional help</h3>
        <div className={styles.sample}>
          <PropertyList
            layout="stacked"
            rows={[
              {
                label: "Listen address",
                value: "127.0.0.1:7433",
                mono: true,
                help: "Address and port the hub HTTP server binds to.",
              },
              {
                label: "Run dir",
                value: "/Users/someone/Library/Caches/evener/run/per-pid-rendezvous",
                mono: true,
                help: "Per-PID rendezvous files; the hub watches this directory to discover live daemons.",
              },
              { label: "Spawn timeout", value: "30s" },
            ]}
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
