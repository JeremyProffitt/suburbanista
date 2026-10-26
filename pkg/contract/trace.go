package contract

import "encoding/json"

// ReadTrace is redacted in worker memory and is never part of persisted snapshots.
type ReadTrace struct {
	Method          string            `json:"method"`
	Endpoint        string            `json:"endpoint"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	Request         json.RawMessage   `json:"request"`
	HTTPStatus      int               `json:"httpStatus"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	Response        json.RawMessage   `json:"response"`
	DurationMS      int64             `json:"durationMs"`
	Truncated       bool              `json:"truncated"`
	Redacted        bool              `json:"redacted"`
}
