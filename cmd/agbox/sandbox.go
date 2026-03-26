package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

type sandboxClient interface {
	CreateSandbox(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	ListSandboxes(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	GetSandbox(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	DeleteSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	DeleteSandboxes(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
}

func runSandbox(ctx context.Context, args []string, stdout io.Writer, lookupEnv func(string) (string, bool)) error {
	if len(args) == 0 {
		return usageErrorf(
			"sandbox command requires a subcommand\navailable subcommands: %s",
			strings.Join(sandboxSubcommands, ", "),
		)
	}
	switch args[0] {
	case "create", "list", "get", "delete":
	default:
		return usageErrorf(
			"unknown sandbox command %q\navailable subcommands: %s",
			args[0],
			strings.Join(sandboxSubcommands, ", "),
		)
	}

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath)
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	return runSandboxWithClient(ctx, client, args, stdout)
}

func runSandboxWithClient(ctx context.Context, client sandboxClient, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return usageErrorf(
			"sandbox command requires a subcommand\navailable subcommands: %s",
			strings.Join(sandboxSubcommands, ", "),
		)
	}

	switch args[0] {
	case "create":
		return runSandboxCreate(ctx, client, args[1:], stdout)
	case "list":
		return runSandboxList(ctx, client, args[1:], stdout)
	case "get":
		return runSandboxGet(ctx, client, args[1:], stdout)
	case "delete":
		return runSandboxDelete(ctx, client, args[1:], stdout)
	default:
		return usageErrorf(
			"unknown sandbox command %q\navailable subcommands: %s",
			args[0],
			strings.Join(sandboxSubcommands, ", "),
		)
	}
}

func runSandboxCreate(ctx context.Context, client sandboxClient, args []string, stdout io.Writer) error {
	parsed, err := parseSandboxCreateArgs(args)
	if err != nil {
		return err
	}

	response, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		CreateSpec: &agboxv1.CreateSpec{
			Image:  parsed.image,
			Labels: parsed.labels,
		},
	})
	if err != nil {
		return runtimeErrorf("create sandbox: %v", err)
	}

	if parsed.json {
		data, err := formatSandboxCreateResponse(response)
		if err != nil {
			return runtimeErrorf("format create sandbox response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprintf(
		stdout,
		"sandbox_id=%s initial_state=%s\n",
		response.GetSandboxId(),
		response.GetInitialState(),
	)
	return nil
}

func runSandboxList(ctx context.Context, client sandboxClient, args []string, stdout io.Writer) error {
	parsed, err := parseSandboxListArgs(args)
	if err != nil {
		return err
	}

	response, err := client.ListSandboxes(ctx, &agboxv1.ListSandboxesRequest{
		IncludeDeleted: parsed.includeDeleted,
		LabelSelector:  parsed.labels,
	})
	if err != nil {
		return runtimeErrorf("list sandboxes: %v", err)
	}

	if parsed.json {
		data, err := formatSandboxListResponse(response)
		if err != nil {
			return runtimeErrorf("format list sandboxes response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprint(stdout, formatSandboxListTable(response.GetSandboxes()))
	return nil
}

func runSandboxGet(ctx context.Context, client sandboxClient, args []string, stdout io.Writer) error {
	parsed, err := parseSandboxGetArgs(args)
	if err != nil {
		return err
	}

	response, err := client.GetSandbox(ctx, parsed.sandboxID)
	if err != nil {
		return runtimeErrorf("get sandbox: %v", err)
	}

	if parsed.json {
		data, err := formatSandboxGetResponse(response)
		if err != nil {
			return runtimeErrorf("format get sandbox response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	text, err := formatSandboxHandleText(response.GetSandbox())
	if err != nil {
		return runtimeErrorf("format sandbox handle: %v", err)
	}
	_, _ = fmt.Fprint(stdout, text)
	return nil
}

func runSandboxDelete(ctx context.Context, client sandboxClient, args []string, stdout io.Writer) error {
	parsed, err := parseSandboxDeleteArgs(args)
	if err != nil {
		return err
	}
	if parsed.json {
		return usageErrorf("sandbox delete does not support --json")
	}
	if parsed.sandboxID != "" && len(parsed.labels) > 0 {
		return usageErrorf("sandbox delete <sandbox_id> and --label are mutually exclusive")
	}
	if parsed.sandboxID == "" && len(parsed.labels) == 0 {
		return usageErrorf("sandbox delete requires <sandbox_id> or at least one --label")
	}

	if parsed.sandboxID != "" {
		response, err := client.DeleteSandbox(ctx, parsed.sandboxID)
		if err != nil {
			return runtimeErrorf("delete sandbox: %v", err)
		}
		_, _ = fmt.Fprint(stdout, formatSandboxDeleteAccepted(response))
		return nil
	}

	response, err := client.DeleteSandboxes(ctx, &agboxv1.DeleteSandboxesRequest{
		LabelSelector: parsed.labels,
	})
	if err != nil {
		return runtimeErrorf("delete sandboxes: %v", err)
	}
	_, _ = fmt.Fprint(stdout, formatSandboxDeleteByLabel(response))
	return nil
}
