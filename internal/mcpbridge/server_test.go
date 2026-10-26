package mcpbridge

import (
	"context"
	"encoding/json"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"time"
)

type apiFixture struct {
	available      map[string]bool
	reads, submits int
	ids            []string
	statusIDs      []string
}

func (a *apiFixture) Features(context.Context) (contract.FeaturesResponse, error) {
	out := contract.FeaturesResponse{Features: contract.FeatureCatalog()}
	for id, available := range a.available {
		out.Availability = append(out.Availability, contract.FeatureAvailability{ID: id, Available: available})
	}
	return out, nil
}
func (a *apiFixture) Dashboard(context.Context) (map[string]any, error) {
	return map[string]any{"providerState": "ready"}, nil
}
func (a *apiFixture) Status(_ context.Context, id string) (contract.AlexaCommandResponse, error) {
	a.statusIDs = append(a.statusIDs, id)
	return contract.AlexaCommandResponse{State: "not-found"}, nil
}
func (a *apiFixture) Read(_ context.Context, id string, trace bool) (contract.AlexaCommandResponse, error) {
	a.reads++
	return contract.AlexaCommandResponse{State: "succeeded", Snapshot: &contract.VehicleSnapshot{FeatureID: id, State: "available"}}, nil
}
func (a *apiFixture) Submit(_ context.Context, _ string, id string, _ contract.CommandOptions) (contract.AlexaCommandResponse, error) {
	a.submits++
	a.ids = append(a.ids, id)
	return contract.AlexaCommandResponse{State: "queued"}, nil
}
func TestMCPInitializeListReadAndUnavailableRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	api := &apiFixture{available: map[string]bool{"diagnostics": true, "lock": false, "ev-charging-metrics": false}}
	server := New(api)
	st, ct := mcp.NewInMemoryTransports()
	ss, e := server.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	changes := make(chan struct{}, 100)
	client := mcp.NewClient(&mcp.Implementation{Name: "conformance", Version: "1"}, &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changes <- struct{}{} }})
	session, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	list, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	if len(names) != 3 || !names["vehicle_diagnostics"] || names["vehicle_lock"] {
		t.Fatal("unavailable tools were advertised")
	}
	// Allow the first registration notification to arrive, then drain it.
	time.Sleep(100 * time.Millisecond)
	for len(changes) > 0 {
		<-changes
	}
	if _, e = session.ListTools(ctx, nil); e != nil {
		t.Fatal(e)
	}
	select {
	case <-changes:
		t.Fatal("unchanged list mutated registry")
	case <-time.After(100 * time.Millisecond):
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_diagnostics", Arguments: map[string]any{}})
	if e != nil || result.IsError || api.reads != 1 || api.submits != 0 {
		t.Fatal("real MCP protocol read failed", e)
	}
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_lock", Arguments: map[string]any{"request_id": "stable-synthetic-intent"}})
	if e == nil && !result.IsError {
		t.Fatal("unadvertised mutation accepted")
	}
	if api.submits != 0 {
		t.Fatal("unavailable mutation reached API")
	}
	api.available["diagnostics"] = false
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_diagnostics", Arguments: map[string]any{}})
	if e != nil || !result.IsError || api.reads != 1 {
		t.Fatal("per-call availability not rechecked")
	}
	list, e = session.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 2 {
		t.Fatal("dynamic list did not remove unavailable read")
	}
	// These are SDK calls against the in-memory fixture, never the vehicle.
	api.available["lock"] = true
	list, e = session.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 3 {
		t.Fatal("newly available action was not advertised")
	}
	for _, tool := range list.Tools {
		if tool.Name == "vehicle_lock" && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint) {
			t.Fatal("physical action advertised as read-only")
		}
	}
	for _, args := range []map[string]any{
		{},
		{"request_id": "short"},
		{"request_id": "stable-synthetic-intent", "options": map[string]any{"delay": -1}},
		{"request_id": "stable-synthetic-intent", "options": map[string]any{"duration": 10}},
	} {
		result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_lock", Arguments: args})
		if e == nil && !result.IsError {
			t.Fatal("invalid action arguments accepted")
		}
	}
	if api.submits != 0 {
		t.Fatal("invalid arguments reached submit")
	}
	const intent = "stable-synthetic-intent"
	for range 2 {
		result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_lock", Arguments: map[string]any{"request_id": intent}})
		if e != nil || result.IsError {
			t.Fatal("fixture action failed", e)
		}
	}
	if api.submits != 2 || len(api.ids) != 2 || api.ids[0] != intent || api.ids[1] != intent {
		t.Fatal("caller request ID changed or action was automatically retried")
	}
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "latest_command_status", Arguments: map[string]any{"request_id": intent}})
	if e != nil || result.IsError || len(api.statusIDs) != 1 || api.statusIDs[0] != intent {
		t.Fatal("status lookup lost the original request ID")
	}
	api.available["lock"] = false
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "vehicle_lock", Arguments: map[string]any{"request_id": intent}})
	if e != nil || !result.IsError || api.submits != 2 {
		t.Fatal("disabled action reached submit before the next tools/list")
	}
}

func TestArgumentsRejectTrailingJSON(t *testing.T) {
	var value struct {
		RequestID string `json:"request_id"`
	}
	if arguments(json.RawMessage(`{"request_id":"stable-id"} {}`), &value) == nil {
		t.Fatal("trailing payload accepted")
	}
}
