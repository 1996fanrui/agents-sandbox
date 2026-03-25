package docker

const LabelNamespace = "io.github.1996fanrui.agents-sandbox"

const (
	LabelSandboxID   = LabelNamespace + ".sandbox-id"
	LabelComponent   = LabelNamespace + ".component"
	LabelServiceName = LabelNamespace + ".service-name"
	LabelProfile     = LabelNamespace + ".profile"
)

func SandboxLabels(sandboxID string, profile string) map[string]string {
	return map[string]string{
		LabelSandboxID: sandboxID,
		LabelComponent: "primary",
		LabelProfile:   profile,
	}
}

func ServiceLabels(sandboxID string, serviceName string) map[string]string {
	return map[string]string{
		LabelSandboxID:   sandboxID,
		LabelComponent:   "service",
		LabelServiceName: serviceName,
	}
}
