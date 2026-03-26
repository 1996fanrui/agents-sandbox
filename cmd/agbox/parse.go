package main

import (
	"fmt"
	"slices"
	"strings"
)

func parseKeyValueAssignment(raw string, flagName string) (string, string, error) {
	key, value, found := strings.Cut(raw, "=")
	if !found {
		return "", "", usageErrorf("%s must be in key=value form", flagName)
	}
	return key, value, nil
}

func parseLabelAssignment(raw string) (string, string, error) {
	return parseKeyValueAssignment(raw, "--label")
}

type sandboxCreateArgs struct {
	image  string
	labels map[string]string
	json   bool
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

type keyValuePair struct {
	key   string
	value string
}

type sandboxExecArgs struct {
	sandboxID    string
	cwd          string
	envOverrides []keyValuePair
	command      []string
}

func parseSandboxCreateArgs(args []string) (sandboxCreateArgs, error) {
	var parsed sandboxCreateArgs
	parsed.labels = make(map[string]string)

	for index := 0; index < len(args); {
		switch args[index] {
		case "--image":
			if index+1 >= len(args) {
				return sandboxCreateArgs{}, usageErrorf("sandbox create requires --image <image>")
			}
			parsed.image = args[index+1]
			index += 2
		case "--label":
			if index+1 >= len(args) {
				return sandboxCreateArgs{}, usageErrorf("--label must be in key=value form")
			}
			key, value, err := parseLabelAssignment(args[index+1])
			if err != nil {
				return sandboxCreateArgs{}, err
			}
			parsed.labels[key] = value
			index += 2
		case "--json":
			parsed.json = true
			index++
		default:
			return sandboxCreateArgs{}, usageErrorf("sandbox create does not accept argument %q", args[index])
		}
	}

	if parsed.image == "" {
		return sandboxCreateArgs{}, usageErrorf("sandbox create requires --image <image>")
	}

	return parsed, nil
}

func parseSandboxListArgs(args []string) (sandboxListArgs, error) {
	var parsed sandboxListArgs
	parsed.labels = make(map[string]string)

	for index := 0; index < len(args); {
		switch args[index] {
		case "--include-deleted":
			parsed.includeDeleted = true
			index++
		case "--label":
			if index+1 >= len(args) {
				return sandboxListArgs{}, usageErrorf("--label must be in key=value form")
			}
			key, value, err := parseLabelAssignment(args[index+1])
			if err != nil {
				return sandboxListArgs{}, err
			}
			parsed.labels[key] = value
			index += 2
		case "--json":
			parsed.json = true
			index++
		default:
			return sandboxListArgs{}, usageErrorf("sandbox list does not accept argument %q", args[index])
		}
	}

	return parsed, nil
}

func parseSandboxGetArgs(args []string) (sandboxGetArgs, error) {
	var parsed sandboxGetArgs

	for index := 0; index < len(args); {
		switch args[index] {
		case "--json":
			parsed.json = true
			index++
		default:
			if strings.HasPrefix(args[index], "-") {
				return sandboxGetArgs{}, usageErrorf("sandbox get does not accept argument %q", args[index])
			}
			if parsed.sandboxID != "" {
				return sandboxGetArgs{}, usageErrorf("sandbox get accepts exactly one <sandbox_id>")
			}
			parsed.sandboxID = args[index]
			index++
		}
	}

	if parsed.sandboxID == "" {
		return sandboxGetArgs{}, usageErrorf("sandbox get requires <sandbox_id>")
	}

	return parsed, nil
}

func parseSandboxDeleteArgs(args []string) (sandboxDeleteArgs, error) {
	var parsed sandboxDeleteArgs
	parsed.labels = make(map[string]string)

	for index := 0; index < len(args); {
		switch args[index] {
		case "--json":
			parsed.json = true
			index++
		case "--label":
			if index+1 >= len(args) {
				return sandboxDeleteArgs{}, usageErrorf("--label must be in key=value form")
			}
			key, value, err := parseLabelAssignment(args[index+1])
			if err != nil {
				return sandboxDeleteArgs{}, err
			}
			parsed.labels[key] = value
			index += 2
		default:
			if strings.HasPrefix(args[index], "-") {
				return sandboxDeleteArgs{}, usageErrorf("sandbox delete does not accept argument %q", args[index])
			}
			if parsed.sandboxID != "" {
				return sandboxDeleteArgs{}, usageErrorf("sandbox delete accepts exactly one <sandbox_id>")
			}
			parsed.sandboxID = args[index]
			index++
		}
	}

	return parsed, nil
}

func parseSandboxExecArgs(args []string) (sandboxExecArgs, error) {
	var parsed sandboxExecArgs

	if len(args) == 0 {
		return sandboxExecArgs{}, usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
	}
	if strings.HasPrefix(args[0], "-") {
		return sandboxExecArgs{}, usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
	}
	parsed.sandboxID = args[0]

	for index := 1; index < len(args); {
		switch args[index] {
		case "--":
			parsed.command = append([]string(nil), args[index+1:]...)
			if len(parsed.command) == 0 {
				return sandboxExecArgs{}, usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
			}
			return parsed, nil
		case "--cwd":
			if index+1 >= len(args) {
				return sandboxExecArgs{}, usageErrorf("sandbox exec requires --cwd <dir>")
			}
			parsed.cwd = args[index+1]
			index += 2
		case "--env":
			if index+1 >= len(args) {
				return sandboxExecArgs{}, usageErrorf("--env must be in key=value form")
			}
			key, value, err := parseKeyValueAssignment(args[index+1], "--env")
			if err != nil {
				return sandboxExecArgs{}, err
			}
			parsed.envOverrides = append(parsed.envOverrides, keyValuePair{key: key, value: value})
			index += 2
		case "--json":
			return sandboxExecArgs{}, usageErrorf("sandbox exec does not support --json")
		default:
			if strings.HasPrefix(args[index], "-") {
				return sandboxExecArgs{}, usageErrorf("sandbox exec does not accept argument %q", args[index])
			}
			return sandboxExecArgs{}, usageErrorf("sandbox exec requires -- <command> [args...]")
		}
	}

	return sandboxExecArgs{}, usageErrorf("sandbox exec requires -- <command> [args...]")
}

func labelsToPairs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return pairs
}
