package ownerclient

import "time"

type CommandVerification struct {
	RequestID          string
	Action             string
	ProviderCommandID  string
	ProviderState      string
	ProviderReason     string
	ProviderUpdatedAt  int64
	RecordedAt         time.Time
	PhysicalObserved   *bool
	PhysicalObservedAt *time.Time
}

func AppendVerification(records []CommandVerification, intent CommandIntent) []CommandVerification {
	out := append([]CommandVerification{}, records...)
	out = append(out, CommandVerification{RequestID: intent.RequestID, Action: intent.Action, ProviderState: "prepared", RecordedAt: time.Now().UTC()})
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	return out
}
func VerificationResult(records []CommandVerification, id string, result CommandResult) ([]CommandVerification, bool) {
	out := append([]CommandVerification{}, records...)
	for i := range out {
		if out[i].RequestID == id {
			if out[i].ProviderState == result.State && out[i].ProviderUpdatedAt == result.UpdatedAt && out[i].ProviderReason == result.Reason {
				return records, false
			}
			out[i].ProviderState = result.State
			out[i].ProviderReason = result.Reason
			out[i].ProviderCommandID = result.CommandID
			out[i].ProviderUpdatedAt = result.UpdatedAt
			return out, true
		}
	}
	return records, false
}
