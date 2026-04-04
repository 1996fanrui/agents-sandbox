package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var protoJSONOptions = protojson.MarshalOptions{
	UseProtoNames:   true,
	EmitUnpopulated: true,
	Indent:          "  ",
}

func writeProtoJSON(message proto.Message) (string, error) {
	data, err := protoJSONOptions.Marshal(message)
	if err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return "", err
	}
	return pretty.String(), nil
}

func formatSandboxCreateResponse(resp *agboxv1.CreateSandboxResponse) (string, error) {
	return writeProtoJSON(resp)
}

func formatSandboxListResponse(resp *agboxv1.ListSandboxesResponse) (string, error) {
	return writeProtoJSON(resp)
}

func formatSandboxGetResponse(resp *agboxv1.GetSandboxResponse) (string, error) {
	return writeProtoJSON(resp)
}

// relativeTime returns a human-friendly relative time string like "5m ago", "3h ago", "2d ago".
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// relativeAge returns a compact relative duration like "2h", "5m", "1d".
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// humanStateName converts a SandboxState enum to a human-friendly name.
// E.g. SANDBOX_STATE_READY -> "Ready", SANDBOX_STATE_FAILED -> "Failed"
func humanStateName(state agboxv1.SandboxState) string {
	name := state.String()
	// Strip "SANDBOX_STATE_" prefix
	const prefix = "SANDBOX_STATE_"
	if strings.HasPrefix(name, prefix) {
		name = name[len(prefix):]
	}
	// Title case: first letter upper, rest lower
	if len(name) == 0 {
		return name
	}
	return strings.ToUpper(name[:1]) + strings.ToLower(name[1:])
}

func formatSandboxListTable(handles []*agboxv1.SandboxHandle) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintln(writer, "SANDBOX ID\tCREATED\tSTATUS\tLABELS\tERROR")
	for _, handle := range handles {
		created := ""
		if ts := handle.GetCreatedAt(); ts != nil {
			created = relativeTime(ts.AsTime())
		}
		status := humanStateName(handle.GetState())
		if ts := handle.GetStateChangedAt(); ts != nil {
			status += " " + relativeAge(ts.AsTime())
		}
		errorMsg := handle.GetErrorMessage()
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			handle.GetSandboxId(),
			created,
			status,
			strings.Join(labelsToPairs(handle.GetLabels()), ","),
			errorMsg,
		)
	}

	_ = writer.Flush()
	return buffer.String()
}

func formatSandboxHandleText(handle *agboxv1.SandboxHandle) string {
	createdAt := ""
	if ts := handle.GetCreatedAt(); ts != nil {
		createdAt = ts.AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}

	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "sandbox_id=%s\n", handle.GetSandboxId())
	_, _ = fmt.Fprintf(&builder, "state=%s\n", humanStateName(handle.GetState()))
	_, _ = fmt.Fprintf(&builder, "image=%s\n", handle.GetImage())
	_, _ = fmt.Fprintf(&builder, "created_at=%s\n", createdAt)
	if len(handle.GetLabels()) > 0 {
		_, _ = fmt.Fprintf(&builder, "labels=%s\n", formatStringMapJSON(handle.GetLabels()))
	}
	if handle.GetErrorCode() != "" {
		_, _ = fmt.Fprintf(&builder, "error_code=%s\n", handle.GetErrorCode())
		_, _ = fmt.Fprintf(&builder, "error_message=%s\n", handle.GetErrorMessage())
	}
	return builder.String()
}

func formatExecGetResponse(resp *agboxv1.GetExecResponse) (string, error) {
	return writeProtoJSON(resp)
}

func formatExecListResponse(resp *agboxv1.ListActiveExecsResponse) (string, error) {
	return writeProtoJSON(resp)
}

func formatExecStatusText(status *agboxv1.ExecStatus) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "exec_id=%s\n", status.GetExecId())
	_, _ = fmt.Fprintf(&builder, "sandbox_id=%s\n", status.GetSandboxId())
	_, _ = fmt.Fprintf(&builder, "state=%s\n", humanExecStateName(status.GetState()))
	_, _ = fmt.Fprintf(&builder, "command=%s\n", strings.Join(status.GetCommand(), " "))
	if status.GetCwd() != "" {
		_, _ = fmt.Fprintf(&builder, "cwd=%s\n", status.GetCwd())
	}
	// Only show exit_code in terminal states to avoid proto default 0 being misleading.
	if isTerminalExecState(status.GetState()) {
		_, _ = fmt.Fprintf(&builder, "exit_code=%d\n", status.GetExitCode())
	}
	if status.GetError() != "" {
		_, _ = fmt.Fprintf(&builder, "error=%s\n", status.GetError())
	}
	return builder.String()
}

func formatExecListTable(execs []*agboxv1.ExecStatus) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintln(writer, "EXEC ID\tSANDBOX ID\tSTATE\tCOMMAND")
	for _, exec := range execs {
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\n",
			exec.GetExecId(),
			exec.GetSandboxId(),
			humanExecStateName(exec.GetState()),
			strings.Join(exec.GetCommand(), " "),
		)
	}

	_ = writer.Flush()
	return buffer.String()
}

// humanExecStateName converts an ExecState enum to a human-friendly name.
// E.g. EXEC_STATE_RUNNING -> "Running"
func humanExecStateName(state agboxv1.ExecState) string {
	name := state.String()
	const prefix = "EXEC_STATE_"
	if strings.HasPrefix(name, prefix) {
		name = name[len(prefix):]
	}
	if len(name) == 0 {
		return name
	}
	return strings.ToUpper(name[:1]) + strings.ToLower(name[1:])
}

func formatStringMapJSON(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	var builder strings.Builder
	builder.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(key)
		valueJSON, _ := json.Marshal(values[key])
		builder.Write(keyJSON)
		builder.WriteByte(':')
		builder.Write(valueJSON)
	}
	builder.WriteByte('}')
	return builder.String()
}
