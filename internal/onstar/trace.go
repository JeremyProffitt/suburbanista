package onstar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type readTraceKey struct{}
type ReadTraceCapture struct{ value *contract.ReadTrace }

func WithReadTrace(ctx context.Context) (context.Context, *ReadTraceCapture) {
	capture := &ReadTraceCapture{}
	return context.WithValue(ctx, readTraceKey{}, capture), capture
}
func (c *ReadTraceCapture) Result() *contract.ReadTrace { return c.value }
func readTrace(ctx context.Context, method, endpoint string) *ReadTraceCapture {
	c, _ := ctx.Value(readTraceKey{}).(*ReadTraceCapture)
	if c == nil {
		return nil
	}
	if method == http.MethodPost && endpoint == APIBase+"/mbff/garage/v1" {
		return c
	}
	if method == http.MethodGet && (strings.HasPrefix(endpoint, APIBase+"/api/v1/vh/vehiclehealth/v1/healthstatus/") || strings.HasPrefix(endpoint, APIBase+"/veh/datadelivery/digitaltwin/v1/vehicles/")) {
		return c
	}
	return nil
}

var tracePrivateKey = regexp.MustCompile(`(?i)authorization|cookie|token|secret|password|vin|vehicle.?id|device.?id|account|user.?id|person|owner|customer|subscriber|driver|contact|email|phone|latitude|longitude|coordinate|address|location|position|gps|geo|^lat$|^lon$|^lng$`)
var traceSafeKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,79}$`)
var traceQuoted = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
var traceMeasurement = regexp.MustCompile(`(?i)^(fuel|odometer|oil|tire|tyre|battery|engine|coolant|brake|range|mileage)([_ -][a-z0-9]+)*$`)

func traceJSON(data []byte) json.RawMessage {
	if len(data) > 16384 {
		return json.RawMessage(`{"notice":"response omitted: size limit"}`)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"notice":"non-JSON body omitted"}`)
	}
	var scrub func(any, string, int) any
	scrub = func(v any, key string, depth int) any {
		if depth > 12 {
			return "[depth limit]"
		}
		if tracePrivateKey.MatchString(key) {
			return "[redacted]"
		}
		switch x := v.(type) {
		case map[string]any:
			measurement := false
			for descriptor, item := range x {
				switch strings.ToLower(descriptor) {
				case "name", "displayname", "label", "type", "key", "identifier":
					if text, ok := item.(string); ok {
						if tracePrivateKey.MatchString(text) || privateSnapshotText.MatchString(text) {
							return "[redacted private descriptor]"
						}
						measurement = measurement || traceMeasurement.MatchString(text)
					}
				}
			}
			out := map[string]any{}
			for k, item := range x {
				safeKey := k
				if !traceSafeKey.MatchString(k) || privateSnapshotText.MatchString(k) {
					safeKey = "redacted-key"
				}
				if strings.EqualFold(k, "value") && !measurement {
					out[safeKey] = "[redacted unclassified value]"
				} else {
					out[safeKey] = scrub(item, k, depth+1)
				}
			}
			return out
		case []any:
			out := []any{}
			for i, item := range x {
				if i >= 60 {
					out = append(out, "[remaining items omitted]")
					break
				}
				out = append(out, scrub(item, key, depth+1))
			}
			return out
		case json.Number:
			if len(x.String()) > 10 {
				return "[redacted number]"
			}
			switch strings.ToLower(key) {
			case "value", "year", "fuel", "fuellevel", "odometer", "range", "oillife", "pressure", "batteryvoltage", "code", "status":
				return x
			}
			return "[redacted number]"
		case string:
			if privateSnapshotText.MatchString(x) || len(x) > 120 {
				return "[redacted]"
			}
			switch strings.ToLower(key) {
			case "status":
				switch strings.ToLower(x) {
				case "good", "ok", "warning", "normal", "active", "inactive", "pending", "success", "failure", "failed", "completed", "inprogress", "in_progress":
					return x
				}
			case "unit", "uom":
				switch strings.ToLower(x) {
				case "%", "percent", "mi", "miles", "km", "kilometers", "psi", "kpa", "v", "volts", "c", "f", "celsius", "fahrenheit":
					return x
				}
			case "make", "model":
				switch strings.ToLower(x) {
				case "chevrolet", "chevy", "gmc", "buick", "cadillac", "suburban":
					return x
				}
			case "year":
				if regexp.MustCompile(`^(19|20)[0-9]{2}$`).MatchString(x) {
					return x
				}
			case "cts", "requesttime", "completiontime":
				if _, e := time.Parse(time.RFC3339Nano, x); e == nil {
					return x
				}
			case "name", "displayname":
				if traceMeasurement.MatchString(x) {
					return x
				}
			case "value":
				if regexp.MustCompile(`^[0-9]{1,7}(\.[0-9]{1,3})?$`).MatchString(x) {
					return x
				}
			}
			return "[redacted string]"
		case bool, nil:
			return x
		}
		return "[redacted]"
	}
	clean, _ := json.Marshal(scrub(value, "", 0))
	if len(clean) > 16384 {
		return json.RawMessage(`{"notice":"redacted response omitted: size limit"}`)
	}
	return clean
}
func (c *ReadTraceCapture) record(req *http.Request, status int, headers http.Header, data []byte, start time.Time) {
	endpoint := regexp.MustCompile(`[A-HJ-NPR-Z0-9]{17}`).ReplaceAllString(req.URL.Path, "[vehicle]")
	if req.URL.Query().Get("sms") == "false" && req.URL.Query().Get("region") == "na" {
		endpoint += "?sms=false&region=na"
	}
	request := json.RawMessage(`null`)
	if req.GetBody != nil {
		body, e := req.GetBody()
		if e == nil {
			raw, _ := io.ReadAll(io.LimitReader(body, 16385))
			body.Close()
			if len(raw) <= 16384 {
				if req.URL.Path == "/mbff/garage/v1" {
					encoded, _ := json.Marshal(traceQuoted.ReplaceAllString(string(raw), `"[redacted]"`))
					request = encoded
				} else {
					request = traceJSON(raw)
				}
			}
		}
	}
	responseHeaders := map[string]string{}
	if len(request) > 4096 {
		request = json.RawMessage(`{"notice":"request omitted: size limit"}`)
	}
	if strings.HasPrefix(headers.Get("Content-Type"), "application/json") {
		responseHeaders["Content-Type"] = "application/json"
	}
	c.value = &contract.ReadTrace{Method: req.Method, Endpoint: endpoint, RequestHeaders: map[string]string{"Content-Type": req.Header.Get("Content-Type"), "Accept": "application/json"}, Request: request, HTTPStatus: status, ResponseHeaders: responseHeaders, Response: traceJSON(data), DurationMS: time.Since(start).Milliseconds(), Truncated: len(data) > 16384, Redacted: true}
}
