package appwire

// ManagerSetting is the hosting hub's durable human-controlled preference.
// HubID is a nonsecret authority identity, never a credential.
type ManagerSetting struct {
	HubID    string `json:"hubId"`
	Enabled  bool   `json:"enabled"`
	Revision uint64 `json:"revision"`
}

// ManagerView names the selected thread exactly as thread/read does. Historical
// refs remain addressable independently, even when their persisted workspace
// identity shares one private preference row after Clear.
type ManagerView struct {
	Ref        string         `json:"ref"`
	InstanceID string         `json:"instanceId"`
	Setting    ManagerSetting `json:"setting"`
}

type ThreadManagerSetParams struct {
	Ref     string `json:"ref"`
	Enabled bool   `json:"enabled"`
}

type ThreadManagerSetResponse struct {
	View ManagerView `json:"view"`
}

// ThreadManagerSetErrorData carries the unchanged current view on a refused
// write. View is absent when storage or identity could not be read.
type ThreadManagerSetErrorData struct {
	ErrorData
	View *ManagerView `json:"view,omitempty"`
}
