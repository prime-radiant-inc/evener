package hubcore

// ModelDescriptor is a provider/model pair used by the spawn chip and /api/models.
type ModelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Per-request limits for image attachments. Match the browser-side cap so
// REST and AppWire accept the same image payload surface.
const (
	SendMaxImageItems   = 8
	SendMaxImageBytes   = 8 * 1024 * 1024  // per-image
	SendMaxRequestBytes = 96 * 1024 * 1024 // total request body
)
