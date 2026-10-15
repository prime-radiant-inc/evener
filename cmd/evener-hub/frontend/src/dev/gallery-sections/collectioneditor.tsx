import { useState } from "react";
import { type CollectionAddResult, CollectionEditor } from "../../widgets/collectioneditor";
import { Input } from "../../widgets/input";
import { ThemeFlip } from "../ThemeFlip";

interface DirItem {
  path: string;
}

function LiveCollectionEditor() {
  const [items, setItems] = useState<DirItem[]>([{ path: "/opt/plugins" }, { path: "/home/user/.evener/plugins" }]);

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

// renderAddField: the caller supplies only the field (here a name/value pair
// joined into the widget's draft); the Add button stays the widget's.
function EnvCollectionEditor() {
  const [items, setItems] = useState<string[]>(["LOG_LEVEL=debug"]);
  return (
    <CollectionEditor<string>
      label="Environment"
      items={items}
      getKey={(item) => item}
      renderItem={(item) => item}
      removeLabel={(item) => `Remove ${item}`}
      onRemove={(item) => setItems((current) => current.filter((i) => i !== item))}
      emptyMessage="No environment variables."
      onAdd={(entry) => {
        if (entry.indexOf("=") <= 0) return { ok: false, error: "Use NAME=value." };
        setItems((current) => [...current, entry]);
        return { ok: true };
      }}
      renderAddField={({ value, onChange, disabled }) => {
        const eq = value.indexOf("=");
        const name = eq === -1 ? value : value.slice(0, eq);
        const envValue = eq === -1 ? "" : value.slice(eq + 1);
        return (
          <>
            <Input
              aria-label="Variable name"
              value={name}
              onChange={(e) => onChange(`${e.target.value}=${envValue}`)}
              placeholder="NAME"
              disabled={disabled}
            />
            <Input
              aria-label="Variable value"
              value={envValue}
              onChange={(e) => onChange(`${name}=${e.target.value}`)}
              placeholder="value"
              disabled={disabled}
            />
          </>
        );
      }}
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
      <ThemeFlip>
        <EnvCollectionEditor />
      </ThemeFlip>
    </section>
  );
}
