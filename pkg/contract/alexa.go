package contract

// AlexaCommandRequest crosses only the IAM-protected Alexa-to-worker boundary.
// PersonAccessToken is sensitive: never log, echo, or place it in SQS messages.
type AlexaCommandRequest struct {
	ReadTrace           bool            `json:"readTrace,omitempty"`
	Kind                string          `json:"kind"`
	Source              string          `json:"source,omitempty"`
	FamilyID            string          `json:"familyId,omitempty"`
	Operation           string          `json:"operation"`
	Action              string          `json:"action,omitempty"`
	CommandID           string          `json:"commandId,omitempty"`
	RequestID           string          `json:"requestId"`
	SkillID             string          `json:"skillId"`
	UserID              string          `json:"userId"`
	PersonID            string          `json:"personId,omitempty"`
	PersonAccessToken   string          `json:"personAccessToken,omitempty"`
	Timestamp           string          `json:"timestamp"`
	EventType           string          `json:"eventType,omitempty"`
	EventPublishingTime string          `json:"eventPublishingTime,omitempty"`
	DryRun              bool            `json:"dryRun,omitempty"`
	FeatureID           string          `json:"featureId,omitempty"`
	Options             *CommandOptions `json:"options,omitempty"`
}
type AlexaCommandResponse struct {
	Trace        *ReadTrace            `json:"trace,omitempty"`
	State        string                `json:"state"`
	Reason       string                `json:"reason,omitempty"`
	Action       string                `json:"action,omitempty"`
	CommandID    string                `json:"commandId,omitempty"`
	UpdatedAt    int64                 `json:"updatedAt,omitempty"`
	Snapshot     *VehicleSnapshot      `json:"snapshot,omitempty"`
	Availability []FeatureAvailability `json:"availability,omitempty"`
}
