import { useMemo, useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  Image,
  Pressable,
  ScrollView,
  Text,
  View,
} from "react-native";
import type { DraftDocument } from "./draftDocument";
import type { DraftImage } from "./draftImages";
import type { ImageSelection } from "./imageSelection";
import { ErrorMessage, useColors } from "./ui";

const EMPTY: DraftImage[] = [];
export function ImageAttachments({
  document,
  selection,
  uncertain = false,
  disabled = false,
}: {
  document: Pick<
    DraftDocument,
    "subscribe" | "getSnapshot" | "imagePreviews" | "removeImage"
  >;
  selection: ImageSelection;
  uncertain?: boolean;
  disabled?: boolean;
}) {
  const colors = useColors();
  const draft = useSyncExternalStore(document.subscribe, document.getSnapshot);
  const picking = useSyncExternalStore(
    selection.subscribe,
    selection.getSnapshot,
  );
  const references =
    (uncertain ? draft.record.unconfirmedImages : draft.record.images) ?? EMPTY;
  const previews = useMemo(() => {
    try {
      return { items: document.imagePreviews(references), error: null };
    } catch {
      return { items: [], error: "Could not load saved image previews." };
    }
  }, [document, references]);
  const settled = references.map((image, index) => ({
    ...image,
    uri: previews.items[index]
      ? `data:${image.mediaType};base64,${previews.items[index]?.data}`
      : undefined,
    pending: false,
  }));
  const items = [
    ...settled,
    ...(uncertain
      ? []
      : picking.pending.map((image) => ({ ...image, pending: true }))),
  ];
  if (!items.length && !previews.error) return null;
  return (
    <View>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        keyboardShouldPersistTaps="handled"
        contentContainerStyle={{
          gap: 12,
          paddingVertical: 8,
          paddingHorizontal: 8,
        }}
      >
        {items.map((image) => (
          <View key={image.id} style={{ width: 76 }}>
            <View
              style={{
                width: 76,
                height: 76,
                borderRadius: 12,
                overflow: "hidden",
                backgroundColor: colors.background,
              }}
            >
              {image.uri ? (
                <Image
                  source={{ uri: image.uri }}
                  accessibilityLabel={`Image ${image.marker}: ${image.name ?? "attachment"}`}
                  style={{
                    width: 76,
                    height: 76,
                    opacity: image.pending ? 0.4 : 1,
                  }}
                />
              ) : null}
              {image.pending ? (
                <ActivityIndicator
                  accessibilityLabel={`Processing ${image.name}`}
                  style={{ position: "absolute", inset: 0 }}
                />
              ) : null}
            </View>
            {!uncertain ? (
              <Pressable
                accessibilityRole="button"
                accessibilityState={{ disabled }}
                disabled={disabled}
                accessibilityLabel={`Remove image ${image.marker}: ${image.name ?? "attachment"}`}
                hitSlop={8}
                onPress={() =>
                  image.pending
                    ? selection.remove(image.id)
                    : document.removeImage(image.id)
                }
                style={{
                  position: "absolute",
                  right: -6,
                  top: -6,
                  width: 28,
                  height: 28,
                  borderRadius: 14,
                  alignItems: "center",
                  justifyContent: "center",
                  backgroundColor: colors.surface,
                  borderWidth: 1,
                  borderColor: colors.border,
                }}
              >
                <Text style={{ color: colors.text, fontSize: 20 }}>×</Text>
              </Pressable>
            ) : null}
            <Text
              style={{ color: colors.secondary, fontSize: 12, marginTop: 4 }}
              numberOfLines={1}
            >{`[image ${image.marker}]`}</Text>
          </View>
        ))}
      </ScrollView>
      <ErrorMessage message={previews.error} />
    </View>
  );
}
