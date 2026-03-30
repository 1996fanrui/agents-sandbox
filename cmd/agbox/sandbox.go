package main

import (
	"context"
	"fmt"
	"io"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/protobuf/types/known/durationpb"
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

type sandboxCreateArgs struct {
	image   string
	labels  map[string]string
	idleTTL *time.Duration
	json    bool
}

type sandboxListArgs struct {
	includeDeleted bool
	labels         map[string]string
	json           bool
}

type sandboxGetArgs struct {
	sandboxID string
	json      bool
}

type sandboxDeleteArgs struct {
	sandboxID string
	labels    map[string]string
	json      bool
}

type sandboxExecArgs struct {
	sandboxID    string
	cwd          string
	envOverrides map[string]string
	command      []string
}

func runSandboxCreate(ctx context.Context, client sandboxClient, parsed sandboxCreateArgs, stdout io.Writer) error {
	createSpec := &agboxv1.CreateSpec{
		Image:  parsed.image,
		Labels: parsed.labels,
	}
	if parsed.idleTTL != nil {
		createSpec.IdleTtl = durationpb.New(*parsed.idleTTL)
	}
	response, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		CreateSpec: createSpec,
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

func runSandboxList(ctx context.Context, client sandboxClient, parsed sandboxListArgs, stdout io.Writer) error {
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

func runSandboxGet(ctx context.Context, client sandboxClient, parsed sandboxGetArgs, stdout io.Writer) error {
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

func runSandboxDelete(ctx context.Context, client sandboxClient, parsed sandboxDeleteArgs, stdout io.Writer) error {
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
