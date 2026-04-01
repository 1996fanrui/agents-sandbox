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

	ccLabels := CompanionContainerLabels("sandbox-1", "db", nil)
	if ccLabels[LabelComponent] != "companion" {
		t.Fatalf("unexpected companion container component label: %#v", ccLabels)
	}
	if ccLabels[LabelCompanionContainerName] != "db" {
		t.Fatalf("unexpected companion container label payload: %#v", ccLabels)
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

	ccLabels := CompanionContainerLabels("sandbox-1", "db", userLabels)
	if ccLabels[LabelUserPrefix+"owner"] != "team-a" {
		t.Fatalf("companion container labels missing owner: %#v", ccLabels)
	}
	if ccLabels[LabelCompanionContainerName] != "db" {
		t.Fatalf("companion container labels lost name: %#v", ccLabels)
	}

	userLabels["owner"] = "mutated"
	if sandboxLabels[LabelUserPrefix+"owner"] != "team-a" || ccLabels[LabelUserPrefix+"owner"] != "team-a" {
		t.Fatalf("labels should not alias user input: sandbox=%#v cc=%#v", sandboxLabels, ccLabels)
	}
}
