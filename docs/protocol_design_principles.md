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

- the literal `from_cursor="0"` must replay the full ordered event history since creation

This means the daemon must retain sandbox event history for the supported replay lifetime.

## Ordering

The source of truth is:

- `cursor`
- `sequence`

Do not rely on:

- local receive order
- transport timing
- "response arrived before event"

## SDK Rules

Replay solves "do not miss events".
The SDK still must:

- build a baseline with `GetSandbox`
- deduplicate replayed or stale events
- wait using protocol ordering, not local timing
- re-read authoritative state before declaring success
- compensate for event races with authoritative snapshots when a terminal state may already have been reached

## Subscription Exposure

- Subscription is required as a protocol capability
- The SDK must be able to use it internally
- The SDK should expose subscription as an advanced public API, while `wait=True` remains the default path for ordinary lifecycle usage

## Image Contract

- sandbox image selection is a request-time input
- the daemon must not supply a hidden default primary image
- quickstart and example image strings are documentation values only, not protocol fallbacks
