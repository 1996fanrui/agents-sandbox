# Go SDK Usage

`agents-sandbox` provides a layered Go SDK for callers that want to control the local daemon through the Unix-socket gRPC API.

## What It Is

The Go SDK has two packages:

- `sdk/go/client` is the public high-level SDK for most Go applications.
- `sdk/go/rawclient` is the transport-facing layer for tools that want direct protobuf RPC access.

Both packages talk to the same local daemon and use the same protobuf contract.

## Who Should Use Which Layer

Use `sdk/go/client` when you want:

- public Go types such as `SandboxHandle`, `ExecHandle`, and `SandboxEvent`
- direct-parameter methods instead of protobuf request assembly
- built-in `wait` behavior for slow sandbox and exec operations
- channel-based sandbox event subscription

Use `sdk/go/rawclient` when you want:

- direct access to protobuf request and response messages
- one-to-one RPC wrappers without high-level wait behavior
- typed error translation while keeping the transport contract visible
- a raw event-stream primitive that you control manually

## High-Level Client Example

```go
package main

import (
	"context"
	"fmt"
	"log"

	sdkclient "github.com/1996fanrui/agents-sandbox/sdk/go/client"
)

func main() {
	ctx := context.Background()

	client, err := sdkclient.New()
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	sandbox, err := client.CreateSandbox(
		ctx,
		"ghcr.io/agents-sandbox/coding-runtime:latest",
		sdkclient.WithLabels(map[string]string{"team": "sdk"}),
	)
	if err != nil {
		log.Fatal(err)
	}

	result, err := client.Run(ctx, sandbox.SandboxID, []string{"python", "-c", "print('hello')"})
	if err != nil {
		log.Fatal(err)
	}

	if result.Stdout != nil {
		fmt.Print(*result.Stdout)
	}
}
```

## Raw Client Example

```go
package main

import (
	"context"
	"log"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

func main() {
	ctx := context.Background()

	socketPath, err := rawclient.DefaultSocketPath()
	if err != nil {
		log.Fatal(err)
	}

	client, err := rawclient.New(socketPath)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	response, err := client.ListSandboxes(ctx, &agboxv1.ListSandboxesRequest{
		IncludeDeleted: false,
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("sandboxes=%d", len(response.GetSandboxes()))
}
```

## Stable Behavior

The high-level Go SDK keeps the accepted async contract visible while adding language-appropriate convenience:

- `CreateSandbox`, `ResumeSandbox`, `StopSandbox`, `DeleteSandbox`, and `CancelExec` default to `wait=true`.
- `CreateExec` defaults to `wait=false`.
- `Run` is the direct "wait for terminal exec and read stdout/stderr" path.
- `CreateExec` and `Run` default `cwd` to `/workspace`.
- `SubscribeSandboxEvents` on `sdk/go/client` returns a receive-only channel of `EventOrError`.
- `SubscribeSandboxEvents` on `sdk/go/rawclient` returns the raw stream primitive with `Recv` and `Close`.

Wait paths use the daemon event stream plus authoritative reads:

- sandbox waits begin from the baseline cursor and ignore replayed or stale events
- exec waits combine event-driven wakeups with `GetExec` polling
- overall wait deadlines remain explicit through `context.Context` and the client's operation timeout
- the event stream used by waits and direct subscriptions is also bounded by the client's stream timeout

## Error Handling

Typed SDK errors live in `sdk/go/rawclient`, even when you use the high-level client.

Common patterns:

- use `var notFound *rawclient.SandboxNotFoundError` then `errors.As(err, &notFound)` for missing sandboxes
- use `var notRunning *rawclient.ExecNotRunningError` then `errors.As(err, &notRunning)` for `CancelExec` invalid-state paths
- use `var sdkErr *rawclient.SandboxClientError` then `errors.As(err, &sdkErr)` for generic SDK-level wait and stream failures

## Configuration Notes

- `sdk/go/client.New()` resolves the default daemon socket path automatically.
- `WithTimeout`, `WithStreamTimeout`, and `WithOperationTimeout` let callers tune unary RPC, event-stream, and overall wait deadlines.
- `WithStreamTimeout` defaults to the unary timeout. Long-running waits or direct subscriptions may need a larger stream timeout than the default 5 seconds.
- `WithSocketPath` overrides the default socket path when you need to target a non-default daemon instance.

## Choosing A Default

If you are building an ordinary Go integration, start with `sdk/go/client`.

Reach for `sdk/go/rawclient` only when you explicitly need transport-level control, protobuf-native requests, or a lower-level base for another Go tool.
