package docker

import "testing"

func TestRuntimeLabelsUseReverseDNSNamespace(t *testing.T) {
	sandboxLabels := SandboxLabels("sandbox-1", "default")
	if sandboxLabels[LabelSandboxID] != "sandbox-1" {
		t.Fatalf("unexpected sandbox id label: %#v", sandboxLabels)
	}
	if sandboxLabels[LabelComponent] != "primary" {
		t.Fatalf("unexpected sandbox component label: %#v", sandboxLabels)
	}
	if LabelNamespace != "io.github.1996fanrui.agents-sandbox" {
		t.Fatalf("unexpected label namespace: %s", LabelNamespace)
	}

	serviceLabels := ServiceLabels("sandbox-1", "db")
	if serviceLabels[LabelComponent] != "service" {
		t.Fatalf("unexpected service component label: %#v", serviceLabels)
	}
	if serviceLabels[LabelServiceName] != "db" {
		t.Fatalf("unexpected service label payload: %#v", serviceLabels)
	}
}
