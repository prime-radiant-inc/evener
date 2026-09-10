import * as Clipboard from "expo-clipboard";
import { memo, useMemo } from "react";
import { AccessibilityInfo, Alert, Linking, Platform } from "react-native";
import {
  EnrichedMarkdownText,
  type MarkdownStyle,
} from "react-native-enriched-markdown";
import { externalMarkdownLink } from "./markdownLinks";
import { useColors } from "./ui";

async function copy(text: string) {
  try {
    await Clipboard.setStringAsync(text);
    AccessibilityInfo.announceForAccessibility("Copied");
  } catch {
    Alert.alert("Could not copy", "Select the text and try copying again.");
  }
}
function showLink(target: string) {
  const url = externalMarkdownLink(target);
  Alert.alert(
    url ? "Link" : "Link destination",
    url ? target : `This app cannot open this destination yet.\n\n${target}`,
    [
      ...(url
        ? [
            {
              text: "Open in browser",
              onPress: () => {
                void openLink(url);
              },
            },
          ]
        : []),
      {
        text: "Copy destination",
        onPress: () => {
          void copy(target);
        },
      },
      { text: "Cancel", style: "cancel" },
    ],
  );
}
async function openLink(target: string) {
  const url = externalMarkdownLink(target);
  if (!url) {
    showLink(target);
    return;
  }
  try {
    await Linking.openURL(url);
  } catch {
    Alert.alert("Could not open link", url, [
      {
        text: "Copy destination",
        onPress: () => {
          void copy(target);
        },
      },
      { text: "Cancel", style: "cancel" },
    ]);
  }
}

export const MarkdownResponse = memo(function MarkdownResponse({
  markdown,
}: {
  markdown: string;
}) {
  const colors = useColors();
  const markdownStyle = useMemo<MarkdownStyle>(() => {
    const fontSize = Platform.OS === "ios" ? 17 : 16;
    const body = {
      fontSize,
      lineHeight: fontSize * 1.5,
      color: colors.text,
      marginTop: 0,
      marginBottom: 12,
    };
    const heading = {
      color: colors.text,
      fontWeight: "600",
      marginTop: 20,
      marginBottom: 8,
    };
    return {
      paragraph: body,
      h1: { ...heading, fontSize: 25, lineHeight: 32 },
      h2: { ...heading, fontSize: 22, lineHeight: 29 },
      h3: { ...heading, fontSize: 19, lineHeight: 26 },
      h4: { ...heading, fontSize: 17, lineHeight: 25 },
      h5: { ...heading, fontSize: 17, lineHeight: 25 },
      h6: { ...heading, fontSize: 17, lineHeight: 25 },
      list: {
        ...body,
        bulletColor: colors.secondary,
        markerColor: colors.secondary,
        gapWidth: 8,
        itemSpacing: 4,
      },
      blockquote: {
        ...body,
        color: colors.secondary,
        backgroundColor: colors.background,
        borderColor: colors.border,
        borderWidth: 2,
        gapWidth: 12,
      },
      link: { color: colors.accent, underline: true },
      code: {
        color: colors.text,
        backgroundColor: colors.surface,
        borderColor: colors.border,
        fontSize: 14,
      },
      codeBlock: {
        fontSize: 14,
        lineHeight: 21,
        color: colors.text,
        backgroundColor: colors.surface,
        borderColor: colors.border,
        borderRadius: 10,
        padding: 12,
        marginTop: 8,
        marginBottom: 12,
        syntaxColors: {
          keyword: colors.accent,
          operator: colors.text,
          punctuation: colors.secondary,
          string: colors.background === "#121417" ? "#b8d8a3" : "#2e6443",
          number: colors.background === "#121417" ? "#ecc48d" : "#785119",
          constant: colors.accent,
          comment: colors.secondary,
          function: colors.accent,
          type: colors.accent,
          variable: colors.text,
          property: colors.text,
          tag: colors.accent,
          attribute: colors.accent,
          embedded: colors.text,
        },
      },
      table: {
        ...body,
        fontSize: 14,
        headerTextColor: colors.text,
        headerBackgroundColor: colors.surface,
        rowEvenBackgroundColor: colors.background,
        rowOddBackgroundColor: colors.background,
        borderColor: colors.border,
        borderWidth: 1,
        cellPaddingHorizontal: 12,
        cellPaddingVertical: 10,
      },
      thematicBreak: {
        color: colors.border,
        height: 1,
        marginTop: 16,
        marginBottom: 16,
      },
      taskList: {
        checkedColor: colors.accent,
        checkedTextColor: colors.text,
        borderColor: colors.secondary,
      },
      image: { maxHeight: 320, resizeMode: "contain", borderRadius: 10 },
      math: { color: colors.text, backgroundColor: colors.surface },
      inlineMath: { color: colors.text },
    };
  }, [
    colors.text,
    colors.surface,
    colors.background,
    colors.border,
    colors.accent,
    colors.secondary,
  ]);
  return (
    <EnrichedMarkdownText
      markdown={markdown}
      markdownStyle={markdownStyle}
      flavor="github"
      selectable
      allowFontScaling
      enableTaskListItemToggle={false}
      streamingAnimation={false}
      spoilerOverlay="solid"
      onLinkPress={({ url }) => {
        void openLink(url);
      }}
      onLinkLongPress={({ url }) => showLink(url)}
      contextMenuItems={[
        {
          text: "Copy response",
          onPress: () => {
            void copy(markdown);
          },
        },
      ]}
    />
  );
});
