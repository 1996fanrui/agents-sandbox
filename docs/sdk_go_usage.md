# Go SDK Usage

`agents-sandbox` provides a layered Go SDK for callers that want to control the local daemon through the Unix-socket gRPC API.

## What It Is

The Go SDK has two packages:
- `sdk/go/client` — public high-level SDK for most Go applications.
- `sdk/go/rawclient` — transport-facing layer for tools that want direct protobuf RPC access.

Both talk to the same local daemon and use the same protobuf contract.

## Who Should Use Which Layer

Use `sdk/go/client` when you want: public Go types (`SandboxHandle`, `ExecHandle`, `SandboxEvent`), direct-parameter methods, built-in `wait` behavior, and channel-based event subscription.

Use `sdk/go/rawclient` when you want: direct protobuf request/response access, one-to-one RPC wrappers, typed error translation, and a raw event-stream primitive.

## High-Level Client Example

```go
client, err := sdkclient.New()
if err != nil { log.Fatal(err) }
defer client.Close()

sandbox, err := client.CreateSandbox(ctx,
    sdkclient.WithImage("ghcr.io/agents-sandbox/coding-runtime:latest"),
    sdkclient.WithLabels(map[string]string{"team": "sdk"}),
)
if err != nil { log.Fatal(err) }

result, err := client.Run(ctx, sandbox.SandboxID, []string{"python", "-c", "print('hello')"})
if err != nil { log.Fatal(err) }

if result.StdoutLogPath != nil {
    data, _ := os.ReadFile(*result.StdoutLogPath)
    fmt.Print(string(data))
}
```

## Raw Client Example

```go
socketPath, _ := rawclient.DefaultSocketPath()
client, _ := rawclient.New(socketPath)
defer client.Close()

response, err := client.ListSandboxes(ctx, &agboxv1.ListSandboxesRequest{})
log.Printf("sandboxes=%d", len(response.GetSandboxes()))
```

## Key Types

```go
type SandboxHandle struct {
    SandboxID, Image  string
    State             SandboxState
    LastEventSequence uint64
    RequiredServices, OptionalServices []ServiceSpec
    Labels            map[string]string
    CreatedAt         time.Time
    ErrorCode         *string
    ErrorMessage      *string
    StateChangedAt    *time.Time
}

type ExecHandle struct {
    ExecID, SandboxID string
    State             ExecState
    Command           []string
    Cwd               string          // not *string
    EnvOverrides      map[string]string
    ExitCode          *int32
    Error             *string
    LastEventSequence uint64
    StdoutLogPath, StderrLogPath *string
}
```

`ServiceSpec` uses `Envs` (not `Environment`); `HealthcheckConfig` uses `*time.Duration` for duration fields.

`ListActiveExecs` uses the option pattern: pass `WithSandboxID` to filter by sandbox.

## Stable Behavior

For the full accepted-vs-completed contract and wait semantics, see [Protocol Design Principles](protocol_design_principles.md).

- `CreateSandbox`, `ResumeSandbox`, `StopSandbox`, `DeleteSandbox`, `CancelExec` default to `wait=true`.
- `CreateExec` defaults to `wait=false`.
- `Run` is the direct "wait for terminal exec and return log file paths" path.
- `CreateExec` and `Run` default `cwd` to `/workspace`.
- `SubscribeSandboxEvents` on `sdk/go/client` returns `<-chan EventOrError`; on `sdk/go/rawclient` returns raw stream with `Recv`/`Close`.

Wait paths: sandbox waits begin from baseline event sequence; exec waits seed from `GetExec().LastEventSequence`; exec waits re-read `GetExec` only after relevant post-baseline events; deadlines via `context.Context` and the client's operation timeout; streams bounded by stream timeout.

## Error Handling

Typed SDK errors defined in `sdk/go/rawclient` and re-exported from `sdk/go/client`:

```go
var notFound *client.SandboxNotFoundError
if errors.As(err, &notFound) { ... }

var notRunning *client.ExecNotRunningError
if errors.As(err, &notRunning) { ... }

var sdkErr *client.SandboxClientError
if errors.As(err, &sdkErr) { ... }
```

## Configuration Notes

- `sdk/go/client.New()` resolves the default daemon socket path automatically.
- `WithTimeout`, `WithStreamTimeout`, `WithOperationTimeout` tune unary, event-stream, and overall wait deadlines.
- `WithStreamTimeout` defaults to the unary timeout. Long-running waits may need a larger stream timeout than the default 5 seconds.
- `WithSocketPath` overrides the default socket path.

## Choosing a Default

Start with `sdk/go/client`. Reach for `sdk/go/rawclient` only when you need transport-level control, protobuf-native requests, or a lower-level base for another Go tool.
