package ownerclient

import (
	"context"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"net/url"
)

func (c *Client) RequestLocationRefresh(ctx context.Context, s Session, id string) (contract.LocationRefreshStatus, error) {
	var out contract.LocationRefreshStatus
	err := c.Request(ctx, "POST", "/api/map/refresh", s.AccessHandle, map[string]string{"requestId": id}, &out)
	return out, err
}
func (c *Client) LocationRefreshStatus(ctx context.Context, s Session, id string) (contract.LocationRefreshStatus, error) {
	var out contract.LocationRefreshStatus
	path := "/api/map/refresh/status"
	if id != "" {
		path += "?" + url.Values{"requestId": {id}}.Encode()
	}
	err := c.Request(ctx, "GET", path, s.AccessHandle, nil, &out)
	return out, err
}
