package contract

import "time"

// LocationRefreshStatus contains no coordinates, credential, or storage URL.
// Polling this receipt never requests another provider read.
type LocationRefreshStatus struct {
	RequestID           string    `json:"requestId"`
	OperationID         string    `json:"operationId"`
	State               string    `json:"state"`
	Reason              string    `json:"reason,omitempty"`
	RequestedAt         time.Time `json:"requestedAt"`
	ObservationRevision string    `json:"observationRevision,omitempty"`
	ObservedAt          time.Time `json:"observedAt,omitempty"`
	ReceivedAt          time.Time `json:"receivedAt,omitempty"`
	ImageID             string    `json:"imageId,omitempty"`
	ImageVersion        string    `json:"imageVersion,omitempty"`
	ImageGeneratedAt    time.Time `json:"imageGeneratedAt,omitempty"`
	PollAfterSeconds    int       `json:"pollAfterSeconds"`
}

// LocationRefreshRequest is an IAM-only API-to-worker message. FamilyID is
// resolved by the API, never accepted from request JSON or persisted in SQS.
type LocationRefreshRequest struct {
	Kind      string `json:"kind"`
	FamilyID  string `json:"familyId"`
	RequestID string `json:"requestId"`
}
