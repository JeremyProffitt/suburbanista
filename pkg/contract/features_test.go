package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFeatureCatalogCompleteness(t *testing.T) {
	features := FeatureCatalog()
	seen := map[string]bool{}
	counts := map[string]int{}
	if len(features) != 22 {
		t.Fatal("catalog count")
	}
	for _, f := range features {
		if seen[f.ID] || f.ID == "" || f.SourceMethod == "" || f.Title == "" || f.Section == "" || len(f.Destinations) != 2 || f.Parameters == nil || f.ProofState == "live-confirmed" {
			t.Fatal("invalid catalog row")
		}
		seen[f.ID] = true
		counts[f.Category]++
		if f.Category == "action" && f.Action != f.ID || f.Category == "read" && f.ReadID != f.ID {
			t.Fatal("wire identity mismatch")
		}
	}
	if counts["action"] != 10 || counts["read"] != 8 || counts["ev"] != 4 {
		t.Fatal("category count")
	}
	encoded, _ := json.Marshal(features)
	for _, private := range []string{"accessToken", "refreshToken", "vin", "latitude", "longitude", "deviceId"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private catalog metadata")
		}
	}
}
func TestFeatureOptions(t *testing.T) {
	zero, positive, negative := 0, 1, -1
	if ValidateCommandOptions("lock", CommandOptions{Delay: &zero}) != nil || ValidateCommandOptions("horn-and-lights", CommandOptions{Actions: []string{"Honk"}, Duration: &positive}) != nil {
		t.Fatal("source options rejected")
	}
	for _, c := range []struct {
		action  string
		options CommandOptions
	}{{"start", CommandOptions{Delay: &zero}}, {"lock", CommandOptions{Delay: &negative}}, {"horn-and-lights", CommandOptions{Duration: &zero}}, {"flash-lights", CommandOptions{Actions: []string{"Honk"}}}, {"horn-and-lights", CommandOptions{Actions: []string{"DoorOpen"}}}, {"ev-set-charge-target", CommandOptions{}}} {
		if ValidateCommandOptions(c.action, c.options) == nil {
			t.Fatal("unsafe options accepted")
		}
	}
}
