# Evener for iPhone

<!-- impeccable:product-schema 1 -->

## Platform

ios

## Users

People directing coding agents from their phone. They need to see which sessions need attention, understand the work, answer questions, steer an agent, and use its results without managing runtime machinery.

## Product Purpose

Evener is one product across its runtime, hub, and clients. The iPhone app makes ongoing work readable and actionable away from the computer. The Board prioritizes sessions that need the person; a Session holds the conversation and access to its evidence.

## Operating Context

The iPhone app opens the selected hub’s web workspace in its own persistent WebKit profile. Sessions run on the hub’s machines. The web workspace owns composition, delivery recovery, reading position, and workspace settings. The phone owns its saved connections and system integrations.

## Capabilities and Constraints

- This design work is iPhone-first. Jesse confirmed that iPad and Android remain deferred.
- Preserve existing capabilities, attention ordering, session and project organization, approvals, questions, model selection, settings, and access to activity and evidence.
- Preserve source-of-truth, recovery, draft, and delivery contracts in `../docs/product/` and this directory's README.
- Use the hub’s mobile components for the workspace and Expo SDK 58, SF Symbols, and system controls for pairing, sharing, and native navigation affordances.
- Screenshots from simulator fixtures are design evidence, not proof of real-hub workflow qualification.

## Brand Commitments

Jesse wants a clean, consistent, exceptionally well-designed app aligned with Apple's Human Interface Guidelines and Evener's identity. He delegates visual decisions and allows changes to the style guide and web UI when necessary for coherence. The native app should feel at home on iPhone rather than like a compressed website.

## Product Principles

- Attention is the product: prioritize what needs the person and keep ordinary operation calm.
- Let conversation support understanding, evidence support verification, and controls support intervention.
- Keep consequences visible while progressively disclosing bulky detail.
- Recover automatically where safe; preserve the person's work and honor their chosen boundaries.
- Use familiar platform behavior so visual refinement reduces effort rather than adding new concepts.

## Evidence on Hand

The product guide at `../docs/product/README.md`, shared design model at `../docs/web-ui/design-system.md`, native design record at `DESIGN.md`, and real native implementation define incumbent behavior. The explicitly launched demo hub provides deterministic representative data for visual inspection. Historical screenshots are dated evidence and must be checked against current source.
