package docker

import "testing"

func TestRuntimeLabelsUseReverseDNSNamespace(t *testing.T) {
	sandboxLabels := SandboxLabels("sandbox-1", "aihub|session|session-1", "default")
	if sandboxLabels[LabelSandboxID] != "sandbox-1" {
		t.Fatalf("unexpected sandbox id label: %#v", sandboxLabels)
	}
	if sandboxLabels[LabelComponent] != "primary" {
		t.Fatalf("unexpected sandbox component label: %#v", sandboxLabels)
	}
	if LabelNamespace != "io.github.1996fanrui.agents-sandbox" {
		t.Fatalf("unexpected label namespace: %s", LabelNamespace)
	}

	dependencyLabels := DependencyLabels("sandbox-1", "aihub|session|session-1", "db")
	if dependencyLabels[LabelComponent] != "dependency" {
		t.Fatalf("unexpected dependency component label: %#v", dependencyLabels)
	}
	if dependencyLabels[LabelDependencyName] != "db" {
		t.Fatalf("unexpected dependency label payload: %#v", dependencyLabels)
	}
}
