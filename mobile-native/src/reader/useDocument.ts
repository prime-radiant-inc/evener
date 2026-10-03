// One visible Reader's recovering demand. Bytes and native image events publish
// only for the complete captured identity, never under a replacement's title.
import type { FileReference } from "../../../appwire-client/typescript/fileReferences";
import {
	createDocumentReadDemand,
	type DocumentReadAttempt,
	type DocumentReadOutcome,
} from "../../../appwire-client/typescript/documentReadDemand";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AppState } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { documentNotice, type LoadedDocument } from "./documentSource";
import { readHubDocument } from "./hubDocument";

/** Private viewer event bridge. Each closure captures its source and scheduler attempt. */
export interface NativeImageAttempt {
	generation: string;
	loaded(): void;
	failed(): void;
}

export function useDocument(
	hubId: string,
	sessionRef: string,
	reference: FileReference,
	inFront: boolean,
): {
	document: LoadedDocument | null;
	notice: string | undefined;
	reference: FileReference;
	imageGeneration: string | undefined;
	imageAttempt: NativeImageAttempt | undefined;
	reload(): void;
} {
	const { profiles, state, activeProfile } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin ?? "";
	const capturedReference = useMemo(
		() => ({
			path: reference.path,
			cwd: reference.cwd,
			readTarget: reference.readTarget,
			provenance: reference.provenance,
		}),
		[reference.path, reference.cwd, reference.readTarget, reference.provenance],
	);
	const identity = JSON.stringify([
		hubId,
		origin,
		sessionRef,
		reference.path,
		reference.cwd,
		reference.readTarget,
		reference.provenance,
	]);
	const currentIdentity = useRef(identity);
	currentIdentity.current = identity;
	const [foreground, setForeground] = useState(AppState.currentState === "active");
	const foregroundRef = useRef(foreground);
	const [shown, setShown] = useState<{
		identity: string;
		document: LoadedDocument;
		notice?: string;
		imageAttempt?: NativeImageAttempt;
	} | null>(null);
	const binding = useRef({ hubId, origin, sessionRef, reference, identity });
	const pendingImage = useRef<{ resolve(outcome: DocumentReadOutcome): void } | null>(null);
	const settleImage = useCallback(() => {
		pendingImage.current?.resolve("terminal");
		pendingImage.current = null;
	}, []);
	const demand = useMemo(
		() =>
			createDocumentReadDemand(async (attempt: DocumentReadAttempt) => {
				const source = binding.current;
				const current = () => attempt.isCurrent() && currentIdentity.current === source.identity;
				const next = await readHubDocument(source.origin, source.hubId, source.sessionRef, source.reference.readTarget);
				if (!current()) return "terminal";
				if (next.kind === "image") {
					return new Promise<DocumentReadOutcome>((resolve) => {
						pendingImage.current = { resolve };
						let settled = false;
						const complete = (outcome: "success" | "transient") => {
							if (!current() || settled) return;
							settled = true;
							pendingImage.current = null;
							setShown((previous) =>
								previous?.identity === source.identity
									? {
											...previous,
											notice: outcome === "transient" ? "This image couldn't be loaded right now." : undefined,
										}
									: previous,
							);
							resolve(outcome);
						};
						setShown({
							identity: source.identity,
							document: next,
							imageAttempt: {
								generation: attempt.generation,
								loaded: () => complete("success"),
								failed: () => complete("transient"),
							},
						});
					});
				}
				setShown((previous) =>
					next.kind === "failed" &&
					previous?.identity === source.identity &&
					["markdown", "code", "image", "binary"].includes(previous.document.kind)
						? { ...previous, notice: documentNotice(next) ?? undefined }
						: { identity: source.identity, document: next },
				);
				return next.kind === "failed"
					? "transient"
					: ["missing", "forbidden", "host-unsupported"].includes(next.kind)
						? "terminal"
						: "success";
			}),
		[],
	);

	useEffect(() => {
		binding.current = { hubId, origin, sessionRef, reference: capturedReference, identity };
		demand.replace();
		settleImage();
	}, [hubId, origin, sessionRef, capturedReference, identity, demand, settleImage]);
	const active = inFront && foreground && origin !== "" && activeProfile?.id === hubId && state === "ready";
	useEffect(() => {
		demand.setActive(active);
		if (!active) settleImage();
	}, [active, demand, settleImage]);
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (next) => {
			const nextForeground = next === "active";
			if (nextForeground && foregroundRef.current && active) {
				demand.refresh();
				settleImage();
			}
			foregroundRef.current = nextForeground;
			setForeground(nextForeground);
		});
		return () => subscription.remove();
	}, [active, demand, settleImage]);
	useEffect(
		() => () => {
			demand.dispose();
			settleImage();
		},
		[demand, settleImage],
	);
	const reload = useCallback(() => {
		demand.refresh();
		settleImage();
	}, [demand, settleImage]);
	const visible = shown?.identity === identity ? shown : null;
	return {
		document: visible?.document ?? null,
		notice: visible?.notice,
		reference,
		imageGeneration: visible?.imageAttempt?.generation,
		imageAttempt: visible?.imageAttempt,
		reload,
	};
}
