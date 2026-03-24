package control

type ProjectionMode string

const (
	ProjectionModeBind       ProjectionMode = "bind"
	ProjectionModeShadowCopy ProjectionMode = "shadow_copy"
)

type CapabilityMode string

const (
	CapabilityModeReadOnly  CapabilityMode = "read_only"
	CapabilityModeReadWrite CapabilityMode = "read_write"
	CapabilityModeSocket    CapabilityMode = "socket"
)
