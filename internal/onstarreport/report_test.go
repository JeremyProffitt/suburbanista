package onstarreport

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"strings"
	"testing"
)

type readerFixture struct{ calls int }

func (r *readerFixture) Read(_ context.Context, id string, trace bool) (contract.AlexaCommandResponse, error) {
	r.calls++
	return contract.AlexaCommandResponse{Snapshot: &contract.VehicleSnapshot{FeatureID: id, State: "partial"}, Trace: &contract.ReadTrace{Redacted: true, HTTPStatus: 200, Response: json.RawMessage(`{"value":"<script>bad</script>"}`)}}, nil
}
func TestReportEscapesActualReadTracesAndLabelsNonexecutedCommands(t *testing.T) {
	r := &readerFixture{}
	report := Build(context.Background(), r)
	if r.calls != 8 || report.Captured != 8 || len(report.Cards) != 11 {
		t.Fatal("wrong read/report coverage")
	}
	var out bytes.Buffer
	if Render(&out, report) != nil {
		t.Fatal("render failed")
	}
	html := out.String()
	if strings.Contains(html, "<script>bad") || !strings.Contains(html, "NOT EXECUTED") || (!strings.Contains(html, "&lt;script&gt;") && !strings.Contains(html, `\u003cscript`)) {
		t.Fatal("unsafe or misleading HTML")
	}
}

func TestMetadataContainsNamesAndUnitsNeverReadings(t *testing.T) {
	fields := []contract.SnapshotField{{Key: "diagnostic-value", SourceName: "FUEL_LEVEL", Value: "73.51", Unit: "%"}, {Key: "diagnostic-value", SourceName: "private name", Value: "private-value"}}
	data, _ := json.Marshal(fieldMetadata("diagnostics", fields))
	if strings.Contains(string(data), "73.51") || strings.Contains(string(data), "private") || !strings.Contains(string(data), "FUEL_LEVEL") {
		t.Fatal("metadata included private readings or lost source name")
	}
}
