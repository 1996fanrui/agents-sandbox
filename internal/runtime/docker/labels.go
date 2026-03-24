package docker

const LabelNamespace = "io.github.1996fanrui.agents-sandbox"

const (
	LabelSandboxID      = LabelNamespace + ".sandbox-id"
	LabelOwner          = LabelNamespace + ".owner"
	LabelComponent      = LabelNamespace + ".component"
	LabelDependencyName = LabelNamespace + ".dependency-name"
	LabelProfile        = LabelNamespace + ".profile"
)

func SandboxLabels(sandboxID string, owner string, profile string) map[string]string {
	return map[string]string{
		LabelSandboxID: sandboxID,
		LabelOwner:     owner,
		LabelComponent: "primary",
		LabelProfile:   profile,
	}
}

func DependencyLabels(sandboxID string, owner string, dependencyName string) map[string]string {
	return map[string]string{
		LabelSandboxID:      sandboxID,
		LabelOwner:          owner,
		LabelComponent:      "dependency",
		LabelDependencyName: dependencyName,
	}
}
