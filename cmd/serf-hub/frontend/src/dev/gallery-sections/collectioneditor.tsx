import { useState } from "react";
import { type CollectionAddResult, CollectionEditor } from "../../widgets/collectioneditor";
import { ThemeFlip } from "../ThemeFlip";

interface DirItem {
  path: string;
}

function LiveCollectionEditor() {
  const [items, setItems] = useState<DirItem[]>([{ path: "/opt/plugins" }, { path: "/home/user/.serf/plugins" }]);

  async function handleAdd(value: string): Promise<CollectionAddResult> {
    if (!value.startsWith("/")) return { ok: false, error: "Path must be absolute." };
    setItems((current) => [...current, { path: value }]);
    return { ok: true };
  }

  return (
    <CollectionEditor<DirItem>
      label="Plugin directories"
      items={items}
      getKey={(item) => item.path}
      renderItem={(item) => item.path}
      removeLabel={(item) => `Remove ${item.path}`}
      onRemove={(item) => setItems((current) => current.filter((i) => i.path !== item.path))}
      emptyMessage="No plugin directories. Add one below."
      addPlaceholder="/opt/plugins"
      onAdd={handleAdd}
    />
  );
}

export default function CollectionEditorGallerySection() {
  return (
    <section>
      <h2>CollectionEditor</h2>
      <ThemeFlip>
        <LiveCollectionEditor />
      </ThemeFlip>
      <ThemeFlip>
        <CollectionEditor<DirItem>
          label="Empty example"
          items={[]}
          getKey={(item) => item.path}
          renderItem={(item) => item.path}
          removeLabel={(item) => `Remove ${item.path}`}
          onRemove={() => {}}
          emptyMessage="No plugin directories. Add one below."
          addPlaceholder="/opt/plugins"
          onAdd={async () => ({ ok: true })}
        />
      </ThemeFlip>
    </section>
  );
}
