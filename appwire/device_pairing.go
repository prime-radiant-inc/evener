package appwire

// Device pairing uses authenticated AppWire for the operator and a bounded HTTP
// bootstrap for clients that do not yet possess the Hub credential.
type DevicePairingCreateParams struct {
	Origin  string `json:"origin"`
	Tailnet bool   `json:"tailnet"`
}
type DevicePairingParams struct {
	Invitation string `json:"invitation"`
}
type DevicePairingApproveParams struct {
	Invitation string `json:"invitation"`
	PublicKey  string `json:"publicKey"`
}
type DevicePairingInvitation struct {
	Invitation string `json:"invitation"`
	Origin     string `json:"origin"`
	Tailnet    bool   `json:"tailnet"`
	ExpiresAt  int64  `json:"expiresAt"`
}
type DevicePairingClaim struct {
	PublicKey  string `json:"publicKey"`
	Client     string `json:"client"`
	Comparison string `json:"comparison"`
}
type DevicePairingStatus struct {
	State       string               `json:"state"`
	Claims      []DevicePairingClaim `json:"claims"`
	ApprovedKey string               `json:"approvedKey,omitempty"`
}
type DevicePairingClaimParams struct {
	Invitation string `json:"invitation"`
	PublicKey  string `json:"publicKey"`
	Client     string `json:"client"`
}
type DevicePairingClaimResponse struct {
	State      string `json:"state"`
	Comparison string `json:"comparison,omitempty"`
	Envelope   string `json:"envelope,omitempty"`
}
