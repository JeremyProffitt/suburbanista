package onstarreport

import (
	"context"
	"encoding/json"
	"html/template"
	"io"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type Reader interface {
	Read(context.Context, string, bool) (contract.AlexaCommandResponse, error)
}
type Card struct {
	Title, State, Reason, JSON string
	Captured                   bool
}
type Report struct {
	Generated string
	Cards     []Card
	Captured  int
	Metadata  []FieldMetadata
}
type FieldMetadata struct {
	FeatureID  string `json:"featureId"`
	SourceName string `json:"sourceName"`
	Unit       string `json:"unit"`
	Present    bool   `json:"present"`
}

var metadataName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,59}$`)

func fieldMetadata(feature string, fields []contract.SnapshotField) []FieldMetadata {
	out := []FieldMetadata{}
	for _, field := range fields {
		if field.Key != "diagnostic-value" || !metadataName.MatchString(field.SourceName) {
			continue
		}
		unit := ""
		switch field.Unit {
		case "%", "percent", "mi", "miles", "km", "kilometers", "psi", "kPa", "V", "volts", "C", "F":
			unit = field.Unit
		}
		out = append(out, FieldMetadata{feature, field.SourceName, unit, field.Value != ""})
	}
	return out
}

func Build(ctx context.Context, client Reader) Report {
	out := Report{Generated: time.Now().UTC().Format(time.RFC3339)}
	for _, feature := range contract.FeatureCatalog() {
		if feature.Category != "read" {
			continue
		}
		response, err := client.Read(ctx, feature.ID, true)
		card := Card{Title: feature.Title, State: "unavailable", Reason: "No provider response captured"}
		if err == nil {
			card.State = response.State
			if response.Snapshot != nil {
				card.State = response.Snapshot.State
				card.Reason = response.Snapshot.Reason
				out.Metadata = append(out.Metadata, fieldMetadata(feature.ID, response.Snapshot.Fields)...)
				for _, item := range response.Snapshot.Items {
					out.Metadata = append(out.Metadata, fieldMetadata(feature.ID, item.Fields)...)
				}
			}
			if response.Trace != nil && response.Trace.Redacted {
				data, _ := json.MarshalIndent(response.Trace, "", "  ")
				card.JSON = string(data)
				card.Captured = true
				out.Captured++
			}
		}
		out.Cards = append(out.Cards, card)
	}
	for _, action := range []string{"start", "lock", "unlock"} {
		data, _ := json.MarshalIndent(map[string]any{"action": action, "requestId": "caller-stable-request-id", "notice": "Template only. NOT EXECUTED. No vehicle response exists for this example."}, "", "  ")
		out.Cards = append(out.Cards, Card{Title: action + " — template, not executed", State: "not executed", Reason: "No vehicle action was performed by this report", JSON: string(data)})
	}
	return out
}

var page = template.Must(template.New("report").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'"><title>Suburban — owner request report</title><style>body{margin:0;background:#edf2f7;color:#111827;font:16px system-ui,sans-serif}main{max-width:1120px;margin:auto;padding:36px 20px}header{background:#102438;color:#fff;padding:30px;border-radius:18px}h1{margin:0 0 12px;font-size:32px}.summary{background:#fff;color:#111827;padding:20px;border-radius:14px;margin:24px 0;border:1px solid #b4c1d0}.card{background:#fff;color:#111827;padding:24px;border-radius:14px;margin:20px 0;border:1px solid #b4c1d0}h2{margin-top:0}.state{font-weight:700}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#0f172a;color:#e2e8f0;padding:20px;border-radius:10px;font:14px ui-monospace,monospace}.muted{background:#fff;color:#374151}@media(prefers-color-scheme:dark){body{background:#0b1220;color:#e5edf5}.summary,.card,.muted{background:#172438;color:#e5edf5;border-color:#51647a}}</style><main><header><h1>Suburban owner request report</h1><p>Real read-only provider exchanges, sanitized before leaving worker memory.</p></header><section class="summary"><strong>{{.Captured}} real exchanges captured</strong><p>Generated {{.Generated}}. Missing responses are unavailable, never substituted with examples. Credentials, vehicle/account identifiers and precise location are redacted. Oversized or non-JSON bodies are omitted.</p><p>Requests to start, lock or unlock are templates only and were not executed.</p></section>{{range .Cards}}<article class="card"><h2>{{.Title}}</h2><p class="state">{{.State}}{{if .Captured}} — real redacted exchange{{end}}</p><p class="muted">{{.Reason}}</p>{{if .JSON}}<pre>{{.JSON}}</pre>{{end}}</article>{{end}}</main></html>`))

func Render(w io.Writer, report Report) error { return page.Execute(w, report) }
