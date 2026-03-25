package docker

import "testing"

func TestRuntimeLabelsUseReverseDNSNamespace(t *testing.T) {
	sandboxLabels := SandboxLabels("sandbox-1", "default", nil)
	if sandboxLabels[LabelSandboxID] != "sandbox-1" {
		t.Fatalf("unexpected sandbox id label: %#v", sandboxLabels)
	}
	if sandboxLabels[LabelComponent] != "primary" {
		t.Fatalf("unexpected sandbox component label: %#v", sandboxLabels)
	}
	if LabelNamespace != "io.github.1996fanrui.agents-sandbox" {
		t.Fatalf("unexpected label namespace: %s", LabelNamespace)
	}

	serviceLabels := ServiceLabels("sandbox-1", "db", nil)
	if serviceLabels[LabelComponent] != "service" {
		t.Fatalf("unexpected service component label: %#v", serviceLabels)
	}
	if serviceLabels[LabelServiceName] != "db" {
		t.Fatalf("unexpected service label payload: %#v", serviceLabels)
	}
}

func TestUserLabels(t *testing.T) {
	userLabels := map[string]string{
		"owner": "team-a",
		"env":   "dev",
	}

	sandboxLabels := SandboxLabels("sandbox-1", "default", userLabels)
	if sandboxLabels[LabelUserPrefix+"owner"] != "team-a" {
		t.Fatalf("sandbox labels missing owner: %#v", sandboxLabels)
	}
	if sandboxLabels[LabelUserPrefix+"env"] != "dev" {
		t.Fatalf("sandbox labels missing env: %#v", sandboxLabels)
	}
	if sandboxLabels[LabelSandboxID] != "sandbox-1" || sandboxLabels[LabelProfile] != "default" {
		t.Fatalf("sandbox labels lost system entries: %#v", sandboxLabels)
	}

	serviceLabels := ServiceLabels("sandbox-1", "db", userLabels)
	if serviceLabels[LabelUserPrefix+"owner"] != "team-a" {
		t.Fatalf("service labels missing owner: %#v", serviceLabels)
	}
	if serviceLabels[LabelServiceName] != "db" {
		t.Fatalf("service labels lost service name: %#v", serviceLabels)
	}

	userLabels["owner"] = "mutated"
	if sandboxLabels[LabelUserPrefix+"owner"] != "team-a" || serviceLabels[LabelUserPrefix+"owner"] != "team-a" {
		t.Fatalf("labels should not alias user input: sandbox=%#v service=%#v", sandboxLabels, serviceLabels)
	}
}
