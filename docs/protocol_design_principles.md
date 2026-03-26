# Protocol Design Principles

All slow operations must follow the same contract shape:

- protocol layer: accept the request, expose authoritative state, expose ordered events
- SDK layer: provide `wait` semantics on top of the protocol

This rule applies to create and to all other slow lifecycle operations, for example:

- create sandbox
- stop sandbox
- delete sandbox
- resume sandbox

Current concrete requirement:

- all slow public SDK methods use an explicit `wait` parameter
- `create_sandbox(wait=True)` is the default high-level SDK behavior
- `wait=False` preserves caller-visible async behavior

## Core Split

- Protocol: acceptance, state, ordered events
- SDK: convenience waiting

Do not pretend that an accepted command is already complete.

## Create

Protocol side:

- `CreateSandbox` accepts and returns `sandbox_id`
- `GetSandbox` returns the latest authoritative snapshot
- `SubscribeSandboxEvents` returns ordered lifecycle events

SDK side:

- `create_sandbox(wait=True)` may return only when `READY`
- `create_sandbox(wait=False)` keeps caller-visible async behavior

## Replay Guarantee

For one `sandbox_id`:

- the literal `from_sequence=0` must replay the full ordered event history since creation

This means the daemon must retain sandbox event history for the supported replay lifetime.

## Ordering

The source of truth is the daemon-issued event sequence.

Do not rely on:

- local receive order
- transport timing
- "response arrived before event"

## SDK Rules

Replay solves "do not miss events".
The SDK still must:

- sandbox wait paths build a baseline with `GetSandbox`
- exec wait paths build a baseline with `GetExec`
- deduplicate replayed or stale events
- wait using protocol ordering, not local timing
- re-read authoritative state before declaring success

## Atomic Snapshot-to-Stream Handoff

Do not ask the SDK to "guess" a safe handoff point between a snapshot RPC and
an event subscription.

If a resource supports:

- an authoritative snapshot read, and
- incremental waiting via ordered events

then the protocol must eliminate the handoff race at the source.

Allowed designs:

- the snapshot response carries a daemon-issued event sequence from the same event stream
- the protocol exposes an equivalent atomic subscribe-plus-snapshot primitive

Prohibited design:

- read resource state from one RPC, then borrow a sequence anchor from a different
  resource or a later RPC in order to start subscription

That pattern creates an unfixable race window between "state observed" and
"subscription start". The fix belongs in the protocol contract, not in SDK-side
compensation logic.

## Event Subscription as Sole Wait Mechanism

SDK wait implementations (e.g. `waitForSandboxState`, `waitForExecTerminal`) must use
`SubscribeSandboxEvents` as the sole notification mechanism. Periodic polling tickers
(e.g. 250ms `GetExec` loops) are prohibited as a fallback alongside event subscription.

Rationale: if event subscription is reliable, the ticker is redundant overhead. If it is
unreliable, the correct fix is in the subscription/stream layer, not a polling workaround
that silently masks the failure.

Within one SDK wait or observer flow, if the SDK already has a sandbox event
subscription for the same `sandbox_id` and ordering scope, it should reuse that
subscription instead of opening a duplicate one. Separate callers may still
need separate subscriptions when their sequence anchor, lifetime, or
cancellation boundaries differ.

## gRPC Stream Resilience

When a gRPC event stream disconnects (network interruption, server restart, etc.),
the SDK must handle it by either:

1. **Auto-retry**: re-establish the stream from the last known sequence and resume, or
2. **Explicit failure**: surface the error to the caller so it can reconnect or abort.

Silently falling back to polling on stream failure is prohibited. The stream is the
authoritative notification channel; its failure must be visible, not papered over.

## Exec Wait Implication

If exec waits are driven by sandbox events, the authoritative exec snapshot used by
the wait path must be atomically joinable to that sandbox event stream.

That means at least one of the following must be true:

- `GetExec().exec.last_event_sequence` returns a daemon-issued sandbox event sequence that is valid for
  `SubscribeSandboxEvents`
- the protocol exposes an atomic primitive that returns the exec snapshot and
  starts or seeds the matching event subscription in one step

Without that contract, an SDK cannot remove races by implementation detail alone.

## Subscription Exposure

- Subscription is required as a protocol capability
- The SDK must be able to use it internally
- The SDK should expose subscription as an advanced public API, while `wait=True` remains the default path for ordinary lifecycle usage

## Image Contract

- sandbox image selection is a request-time input
- the daemon must not supply a hidden default primary image
- quickstart and example image strings are documentation values only, not protocol fallbacks
