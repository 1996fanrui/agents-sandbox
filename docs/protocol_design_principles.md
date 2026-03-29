# Protocol Design Principles

All slow operations must follow the same contract shape:

- **Protocol layer**: accept the request, expose authoritative state, expose ordered events
- **SDK layer**: provide `wait` semantics on top of the protocol

This rule applies to all slow lifecycle operations: create, stop, delete, and resume sandbox.

## Accepted-vs-Completed Contract

Slow operations return after acceptance, not after completion. The service then exposes authoritative state through `GetSandbox`/`GetExec` and ordered events through `SubscribeSandboxEvents`. All slow public SDK methods use an explicit `wait` parameter: `create_sandbox(wait=True)` is the default high-level SDK behavior; `wait=False` preserves caller-visible async behavior. Do not pretend that an accepted command is already complete.

## Atomic Snapshot-to-Stream Handoff

If a resource supports both an authoritative snapshot read and incremental waiting via ordered events, the protocol must eliminate the handoff race at the source.

Allowed designs:
- the snapshot response carries a daemon-issued event sequence from the same event stream
- the protocol exposes an equivalent atomic subscribe-plus-snapshot primitive

Prohibited: reading resource state from one RPC, then borrowing a sequence anchor from a different resource or later RPC to start subscription. That pattern creates an unfixable race window.

For exec waits specifically: `GetExec().exec.last_event_sequence` returns a daemon-issued sandbox event sequence valid for `SubscribeSandboxEvents`, letting SDK wait paths subscribe without a handoff race and without fallback polling.

## Ordering

The source of truth is the daemon-issued event sequence. Do not rely on local receive order, transport timing, or "response arrived before event."

## SDK Rules

SDK wait implementations must:
- build sandbox wait baselines with `GetSandbox`; exec wait baselines with `GetExec`
- deduplicate replayed or stale events
- wait using protocol ordering, not local timing
- re-read authoritative state before declaring success

## Event Subscription

`SubscribeSandboxEvents` is the sole wait notification mechanism. Periodic polling tickers (e.g., 250ms `GetExec` loops) are prohibited as a fallback alongside event subscription. If subscription is unreliable, the fix belongs in the subscription/stream layer, not a polling workaround.

Within one SDK wait or observer flow, if the SDK already has a sandbox event subscription for the same `sandbox_id` and ordering scope, it should reuse that subscription instead of opening a duplicate one. Separate callers may still need separate subscriptions when their sequence anchor or cancellation boundaries differ.

Subscription is required as a protocol capability. The SDK must use it internally and should expose it as an advanced public API, while `wait=True` remains the default path for ordinary lifecycle usage.

## gRPC Stream Resilience

When a gRPC event stream disconnects, the SDK must either:

1. **Auto-retry**: re-establish the stream from the last known sequence and resume, or
2. **Explicit failure**: surface the error to the caller so it can reconnect or abort.

Silently falling back to polling on stream failure is prohibited.

## Image Contract

Sandbox image selection is a request-time input. The daemon must not supply a hidden default primary image. Quickstart image strings are documentation values only, not protocol fallbacks.

## SandboxEvent Envelope Model

`SandboxEvent` uses an envelope + oneof model. Each event carries:
- top-level fields: `event_id`, `sequence`, `sandbox_id`, `event_type`, `timestamp`
- a `oneof details` discriminator with one of: `SandboxPhaseDetails` (lifecycle transitions, errors, reason), `ExecEventDetails` (exec state, exit code, errors), or `ServiceEventDetails` (service name, errors)

The top-level `sandbox_state` reflects the sandbox state when the event was emitted. SDKs must dispatch on the active `details` field, not on `event_type` alone.

## Error Model

All gRPC errors carry a `google.rpc.ErrorInfo` detail with:
- `domain`: always `"agents-sandbox"`
- `reason`: machine-readable string constant (e.g., `SANDBOX_NOT_FOUND`, `EXEC_NOT_RUNNING`)
- `metadata`: `map<string, string>` with structured fields such as `sandbox_id` or `exec_id`

SDKs translate `domain` + `reason` into typed exceptions rather than parsing human-readable status messages.

## Public API Version Strategy

APIs are currently pre-GA/preview with possible breaking changes. Post-GA:
- **Proto**: `agbox.v1` baseline; breaking changes use new major namespace (e.g., `agbox.v2`).
- **Go SDK**: breaking changes after `v1.0.0` use major version paths (e.g., `sdk/go/v2`).
- **Python SDK**: semver major version bumps.
