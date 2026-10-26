package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type OwnerAPI interface {
	Features(context.Context) (contract.FeaturesResponse, error)
	Dashboard(context.Context) (map[string]any, error)
	Status(context.Context, string) (contract.AlexaCommandResponse, error)
	Read(context.Context, string, bool) (contract.AlexaCommandResponse, error)
	Submit(context.Context, string, string, contract.CommandOptions) (contract.AlexaCommandResponse, error)
}
type bridge struct {
	api         OwnerAPI
	server      *mcp.Server
	mu          sync.Mutex
	names       []string
	fingerprint string
	initialized bool
}

func New(api OwnerAPI) *mcp.Server {
	b := &bridge{api: api, server: mcp.NewServer(&mcp.Implementation{Name: "suburban-owner", Version: "1.0.0"}, &mcp.ServerOptions{HasTools: true})}
	b.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" || method == "tools/call" {
				if err := b.refresh(ctx, method == "tools/call"); err != nil {
					return nil, err
				}
			}
			return next(ctx, method, request)
		}
	})
	return b.server
}
func result(value any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Owner request was not confirmed. Check sign-in and status; do not retry a mutation with a new request_id."}}}, nil
	}
	data, e := json.Marshal(value)
	if e != nil {
		return nil, errors.New("result unavailable")
	}
	failed := false
	if response, ok := value.(contract.AlexaCommandResponse); ok {
		failed = response.State == "rejected" || response.State == "unconfirmed"
	}
	return &mcp.CallToolResult{IsError: failed, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}
func emptySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}
func (b *bridge) refresh(ctx context.Context, onlyIfUninitialized bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if onlyIfUninitialized && b.initialized {
		return nil
	}
	available, err := b.api.Features(ctx)
	if err != nil {
		b.server.RemoveTools(b.names...)
		b.names = nil
		b.initialized = false
		return errors.New("owner sign-in or feature availability unavailable")
	}
	allowed := map[string]bool{}
	for _, row := range available.Availability {
		allowed[row.ID] = row.Available
	}
	ids := []string{}
	for _, feature := range available.Features {
		canonical, known := contract.FindFeature(feature.ID)
		if known && allowed[feature.ID] && (canonical.Category == "action" || canonical.Category == "read") {
			ids = append(ids, feature.ID)
		}
	}
	sort.Strings(ids)
	fingerprint := strings.Join(ids, "|")
	if b.initialized && fingerprint == b.fingerprint {
		return nil
	}
	b.fingerprint = fingerprint
	b.initialized = true
	b.server.RemoveTools(b.names...)
	b.names = nil
	add := func(name, description string, schema any, readOnly bool, handler mcp.ToolHandler) {
		b.server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: true}}, handler)
		b.names = append(b.names, name)
	}
	add("vehicle_status", "Read the authenticated owner dashboard; this does not operate the vehicle.", emptySchema(), true, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := noArguments(req.Params.Arguments); err != nil {
			return result(nil, err)
		}
		v, e := b.api.Dashboard(ctx)
		return result(v, e)
	})
	statusSchema := emptySchema()
	statusSchema["properties"] = map[string]any{"request_id": map[string]string{"type": "string", "description": "Optional original mutation request_id; omit for the latest owner command."}}
	add("latest_command_status", "Read an existing command outcome. Unknown or unconfirmed is not success.", statusSchema, true, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			RequestID string `json:"request_id"`
		}
		if err := arguments(req.Params.Arguments, &args); err != nil {
			return result(nil, err)
		}
		v, e := b.api.Status(ctx, args.RequestID)
		v.Trace = nil
		return result(v, e)
	})
	for _, feature := range available.Features {
		canonical, known := contract.FindFeature(feature.ID)
		if !known || !allowed[feature.ID] || (canonical.Category != "action" && canonical.Category != "read") {
			continue
		}
		f := canonical
		name := "vehicle_" + strings.ReplaceAll(f.ID, "-", "_")
		if f.Category == "read" {
			add(name, "Read "+f.Title+" from the owner's vehicle. Availability is checked again before the read.", emptySchema(), true, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if e := noArguments(req.Params.Arguments); e != nil {
					return result(nil, e)
				}
				if e := b.require(ctx, f.ID); e != nil {
					return result(nil, e)
				}
				v, e := b.api.Read(ctx, f.ID, false)
				v.Trace = nil
				return result(v, e)
			})
			continue
		}
		properties := map[string]any{"request_id": map[string]any{"type": "string", "minLength": 16, "maxLength": 128, "pattern": "^[A-Za-z0-9_-]+$", "description": "Required stable id for this physical intent. Reuse the SAME id after any uncertain result; never generate a new id merely to retry."}}
		options := map[string]any{}
		for _, p := range f.Parameters {
			schema := map[string]any{"type": "integer"}
			if p.Minimum != nil {
				schema["minimum"] = *p.Minimum
			}
			if p.Maximum != nil {
				schema["maximum"] = *p.Maximum
			}
			if p.Type == "string-list" {
				schema = map[string]any{"type": "array", "uniqueItems": true, "items": map[string]any{"type": "string", "enum": p.Choices}}
			}
			options[p.Name] = schema
		}
		properties["options"] = map[string]any{"type": "object", "properties": options, "additionalProperties": false}
		schema := map[string]any{"type": "object", "properties": properties, "required": []string{"request_id"}, "additionalProperties": false}
		add(name, "Operate the vehicle: "+f.Title+". This is a real physical action, not a preview. Do not call without the owner's request and readiness.", schema, false, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				RequestID string                  `json:"request_id"`
				Options   contract.CommandOptions `json:"options"`
			}
			if e := arguments(req.Params.Arguments, &args); e != nil {
				return result(nil, e)
			}
			if !regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`).MatchString(args.RequestID) || contract.ValidateCommandOptions(f.ID, args.Options) != nil {
				return result(nil, errors.New("invalid command intent"))
			}
			if e := b.require(ctx, f.ID); e != nil {
				return result(nil, e)
			}
			v, e := b.api.Submit(ctx, f.ID, args.RequestID, args.Options)
			v.Trace = nil
			return result(v, e)
		})
	}
	return nil
}
func (b *bridge) require(ctx context.Context, id string) error {
	features, e := b.api.Features(ctx)
	if e != nil {
		return errors.New("availability unavailable")
	}
	for _, row := range features.Availability {
		if row.ID == id && row.Available {
			return nil
		}
	}
	return errors.New("feature unavailable")
}
func arguments(data []byte, out any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errors.New("invalid tool arguments")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid trailing tool arguments")
	}
	return nil
}
func noArguments(data []byte) error { return arguments(data, &struct{}{}) }
