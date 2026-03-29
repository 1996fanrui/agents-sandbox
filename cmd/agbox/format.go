package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var protoJSONOptions = protojson.MarshalOptions{
	UseProtoNames:   true,
	EmitUnpopulated: true,
	Indent:          "  ",
}

var compactProtoJSONOptions = protojson.MarshalOptions{
	UseProtoNames: true,
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

func formatSandboxDeleteAccepted(resp *agboxv1.AcceptedResponse) string {
	return fmt.Sprintf("accepted=%t\n", resp.GetAccepted())
}

func formatSandboxDeleteByLabel(resp *agboxv1.DeleteSandboxesResponse) string {
	return fmt.Sprintf(
		"deleted_count=%d\nsandbox_ids=%s\n",
		resp.GetDeletedCount(),
		strings.Join(resp.GetDeletedSandboxIds(), ","),
	)
}

func formatSandboxListTable(handles []*agboxv1.SandboxHandle) string {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintln(writer, "SANDBOX ID\tSTATE\tLABELS")
	for _, handle := range handles {
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\n",
			handle.GetSandboxId(),
			handle.GetState(),
			strings.Join(labelsToPairs(handle.GetLabels()), ","),
		)
	}

	_ = writer.Flush()
	return buffer.String()
}

func formatSandboxHandleText(handle *agboxv1.SandboxHandle) (string, error) {
	requiredServices, err := formatServiceSpecsJSON(handle.GetRequiredServices())
	if err != nil {
		return "", err
	}
	optionalServices, err := formatServiceSpecsJSON(handle.GetOptionalServices())
	if err != nil {
		return "", err
	}

	createdAt := ""
	if ts := handle.GetCreatedAt(); ts != nil {
		createdAt = ts.AsTime().UTC().Format("2006-01-02T15:04:05Z")
	}

	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "sandbox_id=%s\n", handle.GetSandboxId())
	_, _ = fmt.Fprintf(&builder, "state=%s\n", handle.GetState())
	_, _ = fmt.Fprintf(&builder, "image=%s\n", handle.GetImage())
	_, _ = fmt.Fprintf(&builder, "created_at=%s\n", createdAt)
	_, _ = fmt.Fprintf(&builder, "last_event_sequence=%d\n", handle.GetLastEventSequence())
	_, _ = fmt.Fprintf(&builder, "labels=%s\n", formatStringMapJSON(handle.GetLabels()))
	_, _ = fmt.Fprintf(&builder, "required_services=%s\n", requiredServices)
	_, _ = fmt.Fprintf(&builder, "optional_services=%s\n", optionalServices)
	return builder.String(), nil
}

func formatServiceSpecsJSON(services []*agboxv1.ServiceSpec) (string, error) {
	if len(services) == 0 {
		return "[]", nil
	}

	var builder strings.Builder
	builder.WriteByte('[')
	for index, service := range services {
		if index > 0 {
			builder.WriteByte(',')
		}
		data, err := compactProtoJSONOptions.Marshal(service)
		if err != nil {
			return "", err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return "", err
		}
		builder.Write(compact.Bytes())
	}
	builder.WriteByte(']')
	return builder.String(), nil
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
