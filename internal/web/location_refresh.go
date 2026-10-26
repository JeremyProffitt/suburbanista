package web

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/gofiber/fiber/v3"
)

type LocationRequests interface {
	Request(context.Context, string, string) (contract.LocationRefreshStatus, error)
	Status(context.Context, string, string) (contract.LocationRefreshStatus, error)
}
type IAMLocationRequests struct {
	Client          lambdaInvoker
	Function, Owner string
	Documents       auth.Documents
}

var locationOperationPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s IAMLocationRequests) Request(ctx context.Context, family, id string) (contract.LocationRefreshStatus, error) {
	if s.Client == nil || s.Function == "" {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	payload, _ := json.Marshal(contract.LocationRefreshRequest{Kind: "location-refresh-request", FamilyID: family, RequestID: id})
	out, err := s.Client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(s.Function), Payload: payload})
	if err != nil || out == nil || out.FunctionError != nil || out.StatusCode != 200 || len(out.Payload) > 8192 {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	var result struct {
		Status contract.LocationRefreshStatus `json:"status"`
		Error  string                         `json:"error,omitempty"`
	}
	if json.Unmarshal(out.Payload, &result) != nil {
		return result.Status, auth.ErrUnavailable
	}
	switch result.Error {
	case "":
	case "invalid-request":
		return result.Status, auth.ErrInvalid
	case "session-unavailable":
		return result.Status, auth.ErrHandle
	case "rate-limited":
		return result.Status, auth.ErrRate
	case "conflict":
		return result.Status, auth.ErrConflict
	case "expired":
		return result.Status, auth.ErrExpired
	default:
		return result.Status, auth.ErrUnavailable
	}
	if result.Status.RequestID != id || !locationOperationPattern.MatchString(result.Status.OperationID) || result.Status.RequestedAt.IsZero() || result.Status.PollAfterSeconds != 10 {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	switch result.Status.State {
	case "queued", "collecting", "resolving", "ready", "failed":
	default:
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	return result.Status, nil
}
func (s IAMLocationRequests) Status(ctx context.Context, family, id string) (contract.LocationRefreshStatus, error) {
	if s.Documents == nil {
		return contract.LocationRefreshStatus{}, auth.ErrUnavailable
	}
	if id == "" {
		return locationmap.CurrentStatus(ctx, s.Documents, s.Owner, time.Now().UTC())
	}
	return locationmap.RequestStatus(ctx, s.Documents, s.Owner, family, id, time.Now().UTC())
}
func registerLocationRefresh(app *fiber.App, services Services) {
	authorize := func(c fiber.Ctx) (string, error) {
		c.Set("Cache-Control", "private, no-store")
		c.Set("Referrer-Policy", "no-referrer")
		if services.Mobile == nil || len(c.Request().URI().QueryArgs().PeekMulti("key")) != 0 {
			return "", auth.ErrHandle
		}
		return services.Mobile.Authorize(c.Context(), bearer(c))
	}
	app.Post("/api/map/refresh", func(c fiber.Ctx) error {
		family, err := authorize(c)
		if err != nil {
			return sessionError(c, err)
		}
		if len(c.Request().URI().QueryString()) != 0 {
			return publicError(c, auth.ErrInvalid)
		}
		var body struct {
			RequestID string `json:"requestId"`
		}
		if err = input(c, &body); err != nil {
			return publicError(c, err)
		}
		if !locationmap.ValidRefreshID(body.RequestID) {
			return publicError(c, auth.ErrInvalid)
		}
		if services.LocationRequests == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		ctx, cancel := context.WithTimeout(c.Context(), 6*time.Second)
		defer cancel()
		result, err := services.LocationRequests.Request(ctx, family, body.RequestID)
		if err != nil {
			return publicError(c, err)
		}
		status := 200
		if result.State != "ready" && result.State != "failed" {
			status = 202
		}
		return c.Status(status).JSON(result)
	})
	app.Get("/api/map/refresh/status", func(c fiber.Ctx) error {
		family, err := authorize(c)
		if err != nil {
			return sessionError(c, err)
		}
		if c.Request().URI().QueryArgs().Len() != 0 && (len(c.Request().URI().QueryArgs().PeekMulti("requestId")) != 1 || c.Request().URI().QueryArgs().Len() != 1 || !locationmap.ValidRefreshID(c.Query("requestId"))) {
			return publicError(c, auth.ErrInvalid)
		}
		if services.LocationRequests == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		result, err := services.LocationRequests.Status(c.Context(), family, c.Query("requestId"))
		if err != nil {
			return publicError(c, err)
		}
		return c.JSON(result)
	})
}
