package tool

import "encoding/json"

const (
	VisionRequestedModePurposeDependent   = "purpose_dependent"
	VisionRequestedModeDescription        = "description"
	VisionRequestedModeExactTranscription = "exact_transcription"

	visionExactnessContractOpen  = "<evener:vision_exactness>"
	visionExactnessContractClose = "</evener:vision_exactness>"
)

type visionExactnessContract struct {
	ContractVersion       int    `json:"contract_version"`
	RequestedMode         string `json:"requested_mode"`
	OutputAuthority       string `json:"output_authority"`
	ByteExact             bool   `json:"byte_exact"`
	NormalizationPossible bool   `json:"normalization_possible"`
}

// FormatVisionExactnessContract returns the model-visible machine contract for
// vision requests and results. The contract describes a requested processing
// mode without promising that generated output can satisfy byte-level fidelity.
func FormatVisionExactnessContract(requestedMode string) string {
	payload, err := json.Marshal(visionExactnessContract{
		ContractVersion:       1,
		RequestedMode:         requestedMode,
		OutputAuthority:       "non_authoritative",
		ByteExact:             false,
		NormalizationPossible: true,
	})
	if err != nil {
		return ""
	}
	return visionExactnessContractOpen + string(payload) + visionExactnessContractClose
}
