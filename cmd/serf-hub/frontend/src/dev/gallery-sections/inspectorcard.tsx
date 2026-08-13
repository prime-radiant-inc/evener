import { useState } from "react";
import { InspectorCard } from "../../widgets/inspectorcard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./inspectorcard.module.css";

function LiveInspectorCard() {
  const [strategy, setStrategy] = useState("lora");
  const [precision, setPrecision] = useState("bf16");

  return (
    <InspectorCard
      title="Fine-tune"
      properties={[
        { key: "job", label: "Job ID", value: "ft-2f91c8" },
        {
          key: "strategy",
          label: "Strategy",
          value: strategy,
          options: ["lora", "full", "qlora"],
          onChange: setStrategy,
        },
        {
          key: "precision",
          label: "Precision",
          value: precision,
          options: ["bf16", "fp16", "fp32"],
          onChange: setPrecision,
        },
        { key: "epochs", label: "Epochs", value: "3" },
        { key: "lr", label: "Learning rate", value: "2e-4" },
      ]}
    />
  );
}

export default function InspectorCardGallerySection() {
  return (
    <section>
      <h2>InspectorCard</h2>
      <ThemeFlip>
        <div className={styles.case}>
          <LiveInspectorCard />
        </div>
        <div className={styles.case}>
          <InspectorCard
            title="Session limits"
            properties={[
              { key: "maxTokens", label: "Max tokens", value: "128000" },
              { key: "timeout", label: "Timeout", value: "900s" },
            ]}
          />
        </div>
      </ThemeFlip>
    </section>
  );
}
