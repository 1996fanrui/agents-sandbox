package main

import (
	"bytes"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// TestParseMountFlag covers the host:container[:writable] syntax. Any suffix
// other than the literal ":writable" must be rejected with a hint pointing at
// :writable so users do not accidentally mimic Docker's :ro / :rw vocabulary.
func TestParseMountFlag(t *testing.T) {
	t.Run("two_parts_writable_false", func(t *testing.T) {
		got, err := parseMountFlag("/a:/b")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.GetSource() != "/a" || got.GetTarget() != "/b" || got.GetWritable() {
			t.Fatalf("unexpected MountSpec: %+v", got)
		}
	})

	t.Run("three_parts_writable_true", func(t *testing.T) {
		got, err := parseMountFlag("/a:/b:writable")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.GetSource() != "/a" || got.GetTarget() != "/b" || !got.GetWritable() {
			t.Fatalf("unexpected MountSpec: %+v", got)
		}
	})

	cases := []struct {
		name     string
		input    string
		errFrag  string
		wantHint bool // expects ":writable" hint in error message
	}{
		{"reject_ro", "/a:/b:ro", "writable", true},
		{"reject_rw", "/a:/b:rw", "writable", true},
		{"reject_foo", "/a:/b:foo", "writable", true},
		{"only_colon", ":", "non-empty", false},
		{"empty_source", ":/b", "non-empty", false},
		{"empty_target", "/a:", "non-empty", false},
		{"missing_colon", "/a/b", "host:container", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMountFlag(tc.input)
			if err == nil {
				t.Fatalf("expected error for %q", tc.input)
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Fatalf("error %q missing %q", err.Error(), tc.errFrag)
			}
			if tc.wantHint && !strings.Contains(err.Error(), ":writable") {
				t.Fatalf("error %q missing :writable hint", err.Error())
			}
		})
	}
}

// TestParsePortFlag covers host:container[/proto] syntax with proto defaulting
// to TCP and case-insensitive parsing.
func TestParsePortFlag(t *testing.T) {
	good := []struct {
		name     string
		input    string
		host     uint32
		ctr      uint32
		protocol agboxv1.PortProtocol
	}{
		{"tcp_default", "8080:9090", 8080, 9090, agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		{"udp_lower", "8080:9090/udp", 8080, 9090, agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
		{"sctp_upper", "8080:9090/SCTP", 8080, 9090, agboxv1.PortProtocol_PORT_PROTOCOL_SCTP},
		{"tcp_explicit_upper", "1:65535/TCP", 1, 65535, agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePortFlag(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.GetHostPort() != tc.host || got.GetContainerPort() != tc.ctr || got.GetProtocol() != tc.protocol {
				t.Fatalf("unexpected PortMapping: %+v", got)
			}
		})
	}

	bad := []struct {
		name  string
		input string
	}{
		{"missing_container", "8080"},
		{"container_zero", "8080:0"},
		{"host_zero", "0:9090"},
		{"non_integer", "abc:9090"},
		{"out_of_range", "8080:65536"},
		{"unknown_proto", "8080:9090/foo"},
		{"empty", ""},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePortFlag(tc.input); err == nil {
				t.Fatalf("expected error for %q", tc.input)
			}
		})
	}
}

// TestParseCopyFlag covers the host:container syntax. The split is on the
// first ':' so container paths may contain colons.
func TestParseCopyFlag(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		got, err := parseCopyFlag("/a:/b")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.GetSource() != "/a" || got.GetTarget() != "/b" {
			t.Fatalf("unexpected CopySpec: %+v", got)
		}
	})

	bad := []struct {
		name  string
		input string
	}{
		{"missing_colon", "/a/b"},
		{"empty_source", ":/b"},
		{"empty_target", "/a:"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseCopyFlag(tc.input); err == nil {
				t.Fatalf("expected error for %q", tc.input)
			}
		})
	}
}

// TestResolveAgentSessionArgs_NewFlags exercises the end-to-end path: raw
// flag strings → agentSessionArgs → CreateSpec, asserting both that the flags
// are correctly parsed and that buildAgentCreateSpec splices them into the
// request the daemon receives.
func TestResolveAgentSessionArgs_NewFlags(t *testing.T) {
	v := &agentSessionFlagVars{
		rawCommand: "sleep infinity",
		mounts:     []string{"/host1:/c1", "/host2:/c2:writable"},
		ports:      []string{"8080:9090", "5353:53/udp"},
		copies:     []string{"/src1:/dst1", "/src2:/dst2"},
		labels:     []string{"k1=v1", "k2=v2"},
	}
	parsed, err := resolveAgentSessionArgs(v, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.userMounts) != 2 || parsed.userMounts[0].GetTarget() != "/c1" || !parsed.userMounts[1].GetWritable() {
		t.Fatalf("unexpected userMounts: %+v", parsed.userMounts)
	}
	if len(parsed.userPorts) != 2 || parsed.userPorts[1].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_UDP {
		t.Fatalf("unexpected userPorts: %+v", parsed.userPorts)
	}
	if len(parsed.userCopies) != 2 || parsed.userCopies[0].GetTarget() != "/dst1" {
		t.Fatalf("unexpected userCopies: %+v", parsed.userCopies)
	}
	if parsed.userLabels["k1"] != "v1" || parsed.userLabels["k2"] != "v2" {
		t.Fatalf("unexpected userLabels: %+v", parsed.userLabels)
	}

	spec := buildAgentCreateSpec(parsed, "test", nil, parsed.command)
	if len(spec.GetMounts()) != 2 || len(spec.GetPorts()) != 2 || len(spec.GetCopies()) != 2 {
		t.Fatalf("unexpected spec lengths: mounts=%d ports=%d copies=%d", len(spec.GetMounts()), len(spec.GetPorts()), len(spec.GetCopies()))
	}
	wantLabels := map[string]string{
		"created-by": "agbox-cli",
		"agent-type": "test",
		"k1":         "v1",
		"k2":         "v2",
	}
	for k, want := range wantLabels {
		if got := spec.GetLabels()[k]; got != want {
			t.Fatalf("label %q: want %q got %q", k, want, got)
		}
	}
}

// TestResolveAgentSessionArgs_NewFlags_InvalidLabelKey ensures the empty-key
// check on top of parseKeyValueAssignment is wired in.
func TestResolveAgentSessionArgs_NewFlags_InvalidLabelKey(t *testing.T) {
	_, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		rawCommand: "sleep infinity",
		labels:     []string{"=value"},
	}, "")
	if err == nil {
		t.Fatal("expected error for empty label key")
	}
	if !strings.Contains(err.Error(), "--label key must not be empty") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// agentCommandsForHelpTest returns the five agent command names whose --help
// output must include the new flags. Keep this list in sync with the agent
// subcommand registry in cmd/agbox/main.go.
func agentCommandsForHelpTest() []string {
	return []string{"claude", "codex", "openclaw", "paseo", "agent"}
}

// buildAgentCommandForHelp constructs the cobra command for an agent type so
// that --help renders the same flag set the user would see.
func buildAgentCommandForHelp(t *testing.T, name string) *cobra.Command {
	t.Helper()
	switch name {
	case "claude", "codex", "openclaw":
		return newAgentTypeCommand(name)
	case "paseo":
		return newPaseoTopLevelCommand()
	case "agent":
		return newAgentCommand()
	default:
		t.Fatalf("unknown agent command %q", name)
		return nil
	}
}

// captureHelp returns the --help output of an agent command.
func captureHelp(t *testing.T, name string) string {
	t.Helper()
	cmd := buildAgentCommandForHelp(t, name)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --help on %q: %v", name, err)
	}
	return buf.String()
}

// TestAgentCommandHelp_IncludesNewFlags asserts every agent subcommand's
// --help output advertises the four new flags.
func TestAgentCommandHelp_IncludesNewFlags(t *testing.T) {
	wantFlags := []string{"--mount", "--port", "--copy", "--label"}
	for _, name := range agentCommandsForHelpTest() {
		t.Run(name, func(t *testing.T) {
			out := captureHelp(t, name)
			for _, flag := range wantFlags {
				if !strings.Contains(out, flag) {
					t.Fatalf("agbox %s --help missing %q\n%s", name, flag, out)
				}
			}
		})
	}
}

// TestAgentCommandHelp_NoExcludedFlags asserts excluded flags never appear in
// any agent subcommand's --help output, guarding against accidental future
// regressions (--idle-ttl belongs to sandbox create, --image is preset-only,
// --companion-container is YAML-only per design §5).
func TestAgentCommandHelp_NoExcludedFlags(t *testing.T) {
	excluded := []string{"--idle-ttl", "--image", "--companion-container"}
	for _, name := range agentCommandsForHelpTest() {
		t.Run(name, func(t *testing.T) {
			out := captureHelp(t, name)
			for _, flag := range excluded {
				if strings.Contains(out, flag) {
					t.Fatalf("agbox %s --help unexpectedly contains %q\n%s", name, flag, out)
				}
			}
		})
	}
}

// TestLabelOverride_BuiltinAndUserOrder ensures that user --label values
// overwrite the built-in created-by / agent-type entries (Go map last-write
// semantics) and that repeated --label k=... favours the latest occurrence.
func TestLabelOverride_BuiltinAndUserOrder(t *testing.T) {
	t.Run("user_overrides_builtin", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			labels:     []string{"created-by=other", "agent-type=custom"},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		spec := buildAgentCreateSpec(parsed, "ignored", nil, parsed.command)
		if got := spec.GetLabels()["created-by"]; got != "other" {
			t.Fatalf("created-by: want %q got %q", "other", got)
		}
		if got := spec.GetLabels()["agent-type"]; got != "custom" {
			t.Fatalf("agent-type: want %q got %q", "custom", got)
		}
	})

	t.Run("repeated_label_last_wins", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			labels:     []string{"k=old", "k=new"},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		spec := buildAgentCreateSpec(parsed, "test", nil, parsed.command)
		if got := spec.GetLabels()["k"]; got != "new" {
			t.Fatalf("label k: want %q got %q", "new", got)
		}
	})
}

// TestAgentDefault_PreservesPresets ensures that, with no new flags passed,
// the post-merge CreateSpec for each registered agent type retains the preset
// YAML's mounts/ports verbatim and the built-in labels stay in place. The
// merge is simulated using the same field semantics as
// internal/control.mergeCreateSpecs so the test reflects end-to-end behaviour
// without depending on a daemon process.
func TestAgentDefault_PreservesPresets(t *testing.T) {
	cases := []struct {
		name       string
		agentType  string
		wantMounts []string // mount targets
		wantPorts  []uint32 // host ports
	}{
		{name: "claude", agentType: "claude"},
		{name: "codex", agentType: "codex"},
		{name: "openclaw", agentType: "openclaw", wantMounts: []string{"~/.openclaw"}, wantPorts: []uint32{18789}},
		{name: "paseo", agentType: "paseo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, tc.agentType)
			if err != nil {
				t.Fatalf("resolveAgentSessionArgs: %v", err)
			}
			override := buildAgentCreateSpec(parsed, tc.agentType, nil, parsed.command)
			merged := simulateDaemonMerge(t, parsed.configYaml, override)

			gotTargets := make([]string, 0, len(merged.GetMounts()))
			for _, m := range merged.GetMounts() {
				gotTargets = append(gotTargets, m.GetTarget())
			}
			if !equalStrings(gotTargets, tc.wantMounts) {
				t.Fatalf("%s mounts: want %v got %v", tc.agentType, tc.wantMounts, gotTargets)
			}

			gotPorts := make([]uint32, 0, len(merged.GetPorts()))
			for _, p := range merged.GetPorts() {
				gotPorts = append(gotPorts, p.GetHostPort())
			}
			if !equalUint32(gotPorts, tc.wantPorts) {
				t.Fatalf("%s ports: want %v got %v", tc.agentType, tc.wantPorts, gotPorts)
			}

			if merged.GetLabels()["created-by"] != "agbox-cli" {
				t.Fatalf("%s created-by: %q", tc.agentType, merged.GetLabels()["created-by"])
			}
			if merged.GetLabels()["agent-type"] != tc.agentType {
				t.Fatalf("%s agent-type label: %q", tc.agentType, merged.GetLabels()["agent-type"])
			}
		})
	}
}

// simulateDaemonMerge mirrors internal/control.mergeCreateSpecs (private to
// the daemon package) so the CLI tests can assert end-to-end behaviour. Only
// the fields touched by Stage 2 (mounts/ports/copies/labels) and the preset
// surface (image/command/envs) need to be modelled. Stage 1 already pins the
// authoritative semantics in internal/control unit tests.
func simulateDaemonMerge(t *testing.T, configYAML string, override *agboxv1.CreateSpec) *agboxv1.CreateSpec {
	t.Helper()
	base := loadPresetSpec(t, configYAML)
	if base == nil {
		return proto.Clone(override).(*agboxv1.CreateSpec)
	}
	if override == nil {
		return base
	}
	result := proto.Clone(base).(*agboxv1.CreateSpec)

	if override.GetImage() != "" {
		result.Image = override.GetImage()
	}
	result.Mounts = append(result.Mounts, override.GetMounts()...)
	result.Copies = append(result.Copies, override.GetCopies()...)
	result.Ports = append(result.Ports, override.GetPorts()...)
	if len(override.GetCommand()) > 0 {
		result.Command = append([]string(nil), override.GetCommand()...)
	}
	if len(override.GetLabels()) > 0 {
		if result.Labels == nil {
			result.Labels = make(map[string]string)
		}
		for k, v := range override.GetLabels() {
			result.Labels[k] = v
		}
	}
	if len(override.GetEnvs()) > 0 {
		if result.Envs == nil {
			result.Envs = make(map[string]string)
		}
		for k, v := range override.GetEnvs() {
			result.Envs[k] = v
		}
	}
	return result
}

// loadPresetSpec parses an agent preset YAML into a CreateSpec equivalent.
// It only models the fields the agent presets actually use today (image,
// command, mounts, ports, envs); other fields stay zero.
func loadPresetSpec(t *testing.T, configYAML string) *agboxv1.CreateSpec {
	t.Helper()
	if configYAML == "" {
		return nil
	}
	type yamlMount struct {
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		Writable bool   `yaml:"writable"`
	}
	type yamlPort struct {
		HostPort      uint32 `yaml:"host_port"`
		ContainerPort uint32 `yaml:"container_port"`
		Protocol      string `yaml:"protocol"`
	}
	type yamlCfg struct {
		Image   string            `yaml:"image"`
		Command []string          `yaml:"command"`
		Mounts  []yamlMount       `yaml:"mounts"`
		Ports   []yamlPort        `yaml:"ports"`
		Envs    map[string]string `yaml:"envs"`
	}
	var cfg yamlCfg
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		t.Fatalf("yaml unmarshal preset: %v", err)
	}
	spec := &agboxv1.CreateSpec{
		Image:   cfg.Image,
		Command: cfg.Command,
		Envs:    cfg.Envs,
	}
	for _, m := range cfg.Mounts {
		spec.Mounts = append(spec.Mounts, &agboxv1.MountSpec{
			Source:   m.Source,
			Target:   m.Target,
			Writable: m.Writable,
		})
	}
	for _, p := range cfg.Ports {
		spec.Ports = append(spec.Ports, &agboxv1.PortMapping{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
		})
	}
	return spec
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalUint32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
