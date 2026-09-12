import { type ReactNode, useEffect, useMemo, useState } from "react";
import {
  AppState,
  Platform,
  Text,
  useWindowDimensions,
  View,
} from "react-native";
import { absoluteTime } from "../../cmd/evener-hub/frontend/src/panes/session/chrome/taskTime";
import {
  formatElapsed,
  splitMandate,
} from "../../cmd/evener-hub/frontend/src/panes/session/transcript/messages/format";
import type { ActivityDelegate } from "../../cmd/evener-hub/frontend/src/protocol/activityData";
import {
  delegateModel,
  delegatePacket,
  delegateTiming,
} from "./delegateDetails";
import { MarkdownResponse } from "./MarkdownResponse";
import { Action, Copy, useColors } from "./ui";

function Fact({
  label,
  value,
}: {
  label: string;
  value: string | number | undefined;
}) {
  return value === undefined || value === "" ? null : (
    <View style={{ gap: 2 }}>
      <Copy muted>{label}</Copy>
      <Copy>{value}</Copy>
    </View>
  );
}

function DetailSection({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <View>
      <Action expanded={open} onPress={() => setOpen(!open)}>
        {title}
      </Action>
      {open ? (
        <View style={{ gap: 12, paddingBottom: 12 }}>{children}</View>
      ) : null}
    </View>
  );
}

function Packet({
  value,
  structured = false,
}: {
  value: unknown;
  structured?: boolean;
}) {
  const packet = useMemo(
    () => delegatePacket(value, structured),
    [value, structured],
  );
  const colors = useColors();
  const { fontScale } = useWindowDimensions();
  if (!packet) return <Copy muted>The report could not be displayed.</Copy>;
  if (packet.text === "") return <Copy muted>Empty report.</Copy>;
  if (packet.format === "markdown")
    return <MarkdownResponse markdown={packet.text} />;
  return (
    <Text
      selectable
      allowFontScaling={Platform.OS !== "ios"}
      style={{
        color: colors.text,
        fontFamily: Platform.OS === "ios" ? "Menlo" : "monospace",
        fontSize: 13 * (Platform.OS === "ios" ? fontScale : 1),
        lineHeight: 19 * (Platform.OS === "ios" ? fontScale : 1),
      }}
    >
      {packet.text}
    </Text>
  );
}

function Instruction({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const mandate = useMemo(() => splitMandate(text), [text]);
  if (!mandate) return null;
  return (
    <View style={{ gap: 8 }}>
      <MarkdownResponse markdown={open ? text : mandate.first} />
      {mandate.rest ? (
        <Action expanded={open} onPress={() => setOpen(!open)}>
          {open ? "Show less instruction" : "Show full instruction"}
        </Action>
      ) : null}
    </View>
  );
}

function Messages({ messages }: { messages: string[] }) {
  return [...new Set(messages)].map((message) => (
    <Copy key={message}>{message}</Copy>
  ));
}

/** Optional server facts stay behind disclosures in the owning activity row. */
export function ActivityDelegateDetails({
  delegate,
  connected,
}: {
  delegate: ActivityDelegate;
  connected: boolean;
}) {
  const [now, setNow] = useState(Date.now);
  const timing = delegateTiming(delegate, connected ? now : Number.NaN);
  const clock = connected && (timing.durationLive || timing.quietLive);
  useEffect(() => {
    if (!clock) return;
    let timer: ReturnType<typeof setInterval> | undefined;
    const update = (active: boolean) => {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
      if (active) {
        setNow(Date.now());
        timer = setInterval(() => setNow(Date.now()), 1000);
      }
    };
    update(AppState.currentState === "active");
    const listener = AppState.addEventListener("change", (state) =>
      update(state === "active"),
    );
    return () => {
      if (timer !== undefined) clearInterval(timer);
      listener.remove();
    };
  }, [clock]);

  const model = delegateModel(delegate);
  const instruction = delegate.mandate ?? delegate.task;
  const hasResult = delegate.structuredResult !== undefined;
  const hasResultInfo =
    hasResult ||
    delegate.structuredResultValid !== undefined ||
    !!delegate.structuredResultReason;
  const hasRunDetails = [
    delegate.lifecycle,
    delegate.phase,
    delegate.agentType,
    delegate.resolvedProfileId,
    delegate.resumable,
    delegate.notResumableReason,
    delegate.parentWatchGranted,
    delegate.delegationAllowance,
    delegate.exhaustionBudget,
    delegate.exhaustionLimit,
    delegate.exhaustionResumable,
  ].some((value) => value !== undefined && value !== "");

  return (
    <View style={{ gap: 12 }}>
      {instruction ? <Instruction text={instruction} /> : null}
      <Fact label="Model" value={model.model} />
      <Fact label="Requested model" value={model.requestedModel} />
      <Fact label="Reasoning" value={model.reasoning} />
      <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 16 }}>
        {timing.durationMs !== undefined ? (
          <Fact
            label={
              timing.terminal
                ? "Duration"
                : timing.durationLive
                  ? "Running for"
                  : "Last reported run time"
            }
            value={formatElapsed(timing.durationMs)}
          />
        ) : null}
        {timing.quietForMs !== undefined ? (
          <Fact
            label={timing.quietLive ? "Quiet for" : "Last reported quiet time"}
            value={formatElapsed(timing.quietForMs)}
          />
        ) : null}
      </View>
      <Fact
        label="Started"
        value={timing.startedAt ? absoluteTime(timing.startedAt) : undefined}
      />
      <Fact
        label="Ended"
        value={timing.endedAt ? absoluteTime(timing.endedAt) : undefined}
      />
      {delegate.reason ? (
        <Fact
          label={timing.terminal ? "Reason" : "Previous run reason"}
          value={delegate.reason}
        />
      ) : null}

      {delegate.warnings?.length ? (
        <DetailSection title="Warnings">
          <Messages messages={delegate.warnings} />
        </DetailSection>
      ) : null}
      {delegate.diagnostics?.length ? (
        <DetailSection title="Diagnostics">
          <Messages messages={delegate.diagnostics} />
        </DetailSection>
      ) : null}

      {delegate.usage ? (
        <DetailSection title="Token usage">
          <Copy muted>This session’s own cumulative usage.</Copy>
          <Fact
            label="Input tokens"
            value={delegate.usage.inputTokens.toLocaleString()}
          />
          <Fact
            label="Output tokens"
            value={delegate.usage.outputTokens.toLocaleString()}
          />
          <Fact
            label="Cached input tokens"
            value={delegate.usage.cacheReadTokens?.toLocaleString()}
          />
          <Fact
            label="Total tokens"
            value={delegate.usage.totalTokens?.toLocaleString()}
          />
        </DetailSection>
      ) : null}
      {delegate.worktree ? (
        <DetailSection title="Worktree">
          <Copy muted>Reported with the latest result.</Copy>
          <Fact label="Branch" value={delegate.worktree.branch} />
          <Fact label="Directory" value={delegate.worktree.path} />
          <Fact
            label="Changes"
            value={delegate.worktree.dirty ? "Uncommitted changes" : "Clean"}
          />
          <Fact label="Commits ahead" value={delegate.worktree.ahead} />
          <Fact label="Commit" value={delegate.worktree.headSha} />
        </DetailSection>
      ) : null}
      {delegate.message !== undefined || delegate.packetKind ? (
        <DetailSection title="Latest report">
          <Fact label="Report kind" value={delegate.packetKind} />
          {!timing.terminal ? (
            <Copy muted>The latest report may belong to an earlier run.</Copy>
          ) : null}
          {delegate.message !== undefined ? (
            <Packet value={delegate.message} />
          ) : null}
        </DetailSection>
      ) : null}
      {hasResultInfo ? (
        <DetailSection title="Structured result">
          {!timing.terminal ? (
            <Copy muted>The latest result may belong to an earlier run.</Copy>
          ) : null}
          {delegate.structuredResultValid !== undefined ? (
            <Fact
              label="Validation"
              value={delegate.structuredResultValid ? "Valid" : "Invalid"}
            />
          ) : null}
          <Fact
            label="Validation reason"
            value={delegate.structuredResultReason}
          />
          {hasResult ? (
            <Packet value={delegate.structuredResult} structured />
          ) : null}
        </DetailSection>
      ) : null}
      {hasRunDetails ? (
        <DetailSection title="Run details">
          <Fact label="Lifecycle" value={delegate.lifecycle} />
          <Fact label="Phase" value={delegate.phase} />
          <Fact label="Agent type" value={delegate.agentType} />
          <Fact label="Profile" value={delegate.resolvedProfileId} />
          {delegate.resumable !== undefined ? (
            <Fact
              label="Can resume"
              value={delegate.resumable ? "Yes" : "No"}
            />
          ) : null}
          <Fact label="Resume reason" value={delegate.notResumableReason} />
          {delegate.parentWatchGranted !== undefined ? (
            <Fact
              label="Parent monitoring"
              value={delegate.parentWatchGranted ? "Enabled" : "Disabled"}
            />
          ) : null}
          <Fact
            label="Delegation allowance"
            value={delegate.delegationAllowance}
          />
          <Fact label="Exhausted budget" value={delegate.exhaustionBudget} />
          <Fact label="Budget limit" value={delegate.exhaustionLimit} />
          {delegate.exhaustionResumable !== undefined ? (
            <Fact
              label="Can resume after exhaustion"
              value={delegate.exhaustionResumable ? "Yes" : "No"}
            />
          ) : null}
        </DetailSection>
      ) : null}
    </View>
  );
}
