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

type sandboxExecClient interface {
	sandboxClient
	CreateExec(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	SubscribeSandboxEvents(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	CancelExec(context.Context, string) (*agboxv1.AcceptedResponse, error)
}

func runSandbox(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, lookupEnv func(string) (string, bool)) error {
	subcommand, err := sandboxSubcommand(args)
	if err != nil {
		return err
	}

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	var client *rawclient.RawClient
	if subcommand == "exec" {
		client, err = rawclient.New(socketPath, rawclient.WithTimeout(0))
	} else {
		client, err = rawclient.New(socketPath)
	}
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	return runSandboxWithClient(ctx, client, args, stdout, stderr)
}

func runSandboxWithClient(ctx context.Context, client sandboxExecClient, args []string, stdout io.Writer, stderr io.Writer) error {
	subcommand, err := sandboxSubcommand(args)
	if err != nil {
		return err
	}

	switch subcommand {
	case "create":
		return runSandboxCreate(ctx, client, args[1:], stdout)
	case "list":
		return runSandboxList(ctx, client, args[1:], stdout)
	case "get":
		return runSandboxGet(ctx, client, args[1:], stdout)
	case "delete":
		return runSandboxDelete(ctx, client, args[1:], stdout)
	case "exec":
		return runSandboxExec(ctx, client, args[1:])
	default:
		return usageErrorf("sandbox command dispatcher reached unknown subcommand %q", subcommand)
	}
}

func sandboxSubcommand(args []string) (string, error) {
	if len(args) == 0 {
		return "", usageErrorf(
			"sandbox command requires a subcommand\navailable subcommands: %s",
			strings.Join(sandboxSubcommands, ", "),
		)
	}

	switch args[0] {
	case "create", "list", "get", "delete", "exec":
		return args[0], nil
	default:
		return "", usageErrorf(
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

	text, err := formatSandboxHandleText(response.GetSandbox())
	if err != nil {
		return runtimeErrorf("format create sandbox response: %v", err)
	}
	_, _ = fmt.Fprint(stdout, text)
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
