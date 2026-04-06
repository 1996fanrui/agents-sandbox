package docker

const LabelNamespace = "io.github.1996fanrui.agents-sandbox"

const (
	LabelSandboxID              = LabelNamespace + ".sandbox-id"
	LabelComponent              = LabelNamespace + ".component"
	LabelCompanionContainerName = LabelNamespace + ".companion-container-name"
	LabelProfile                = LabelNamespace + ".profile"
	LabelUserPrefix             = LabelNamespace + ".user."
)

func SandboxLabels(sandboxID string, profile string, userLabels map[string]string) map[string]string {
	labels := map[string]string{
		LabelSandboxID: sandboxID,
		LabelComponent: "primary",
		LabelProfile:   profile,
	}
	return withUserLabels(labels, userLabels)
}

func CompanionContainerLabels(sandboxID string, name string, userLabels map[string]string) map[string]string {
	labels := map[string]string{
		LabelSandboxID:              sandboxID,
		LabelComponent:              "companion",
		LabelCompanionContainerName: name,
	}
	return withUserLabels(labels, userLabels)
}

func withUserLabels(base map[string]string, userLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(base)+len(userLabels))
	for key, value := range base {
		labels[key] = value
	}
	for key, value := range userLabels {
		labels[LabelUserPrefix+key] = value
	}
	return labels
}
