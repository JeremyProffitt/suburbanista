package web

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/lambdahttp"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/gofiber/fiber/v3"
)

type CommandBroker interface {
	Command(context.Context, contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error)
}
type lambdaInvoker interface {
	Invoke(context.Context, *lambda.InvokeInput, ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}
type IAMCommandBroker struct {
	Client   lambdaInvoker
	Function string
}

func (b IAMCommandBroker) Command(ctx context.Context, request contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error) {
	if b.Client == nil || b.Function == "" {
		return contract.AlexaCommandResponse{}, errors.New("command service unavailable")
	}
	payload, _ := json.Marshal(request)
	out, e := b.Client.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(b.Function), Payload: payload})
	if e != nil || out == nil || out.FunctionError != nil || out.StatusCode != 200 || len(out.Payload) > 65536 {
		return contract.AlexaCommandResponse{}, errors.New("command outcome unavailable")
	}
	var result contract.AlexaCommandResponse
	if json.Unmarshal(out.Payload, &result) != nil {
		return result, errors.New("command response invalid")
	}
	return result, nil
}
func mobileCommandRequest(c fiber.Ctx, services Services, operation string) (contract.AlexaCommandRequest, error) {
	family, e := services.Mobile.Authorize(c.Context(), bearer(c))
	if e != nil {
		return contract.AlexaCommandRequest{}, e
	}
	token, e := services.Mobile.CommandFamily(c.Context(), family)
	if e != nil {
		return contract.AlexaCommandRequest{}, e
	}
	return contract.AlexaCommandRequest{Kind: "alexa-command", Source: "mobile", Operation: operation, FamilyID: family, PersonAccessToken: token, RequestID: lambdahttp.RequestMetadata(c.Context()).RequestID, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}
func registerCommands(app *fiber.App, services Services) {
	send := func(c fiber.Ctx, r contract.AlexaCommandRequest) error {
		if services.Commands == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		ctx, cancel := context.WithTimeout(c.Context(), 4500*time.Millisecond)
		defer cancel()
		out, e := services.Commands.Command(ctx, r)
		if e != nil {
			return publicError(c, auth.ErrUnavailable)
		}
		status := 200
		switch out.State {
		case "unconfirmed":
			status = 503
			if out.Reason == "session-revoked" || out.Reason == "owner-authority-unverified" {
				status = 401
			}
		case "accepted", "queued", "dispatching", "submitted":
			status = 202
		case "rejected":
			status = 403
		}
		return c.Status(status).JSON(out)
	}
	app.Post("/api/commands", func(c fiber.Ctx) error {
		var body struct {
			Action    string                  `json:"action"`
			RequestID string                  `json:"requestId"`
			Options   contract.CommandOptions `json:"options,omitempty"`
		}
		if e := input(c, &body); e != nil {
			return publicError(c, e)
		}
		if body.RequestID == "" || len(body.RequestID) > 128 {
			return publicError(c, auth.ErrInvalid)
		}
		if contract.ValidateCommandOptions(body.Action, body.Options) != nil {
			return publicError(c, auth.ErrInvalid)
		}
		r, e := mobileCommandRequest(c, services, "submit")
		if e != nil {
			return sessionError(c, e)
		}
		r.Action = body.Action
		r.RequestID = body.RequestID
		r.Options = &body.Options
		return send(c, r)
	})
	app.Get("/api/commands/status", func(c fiber.Ctx) error {
		if len(c.Request().URI().QueryArgs().PeekMulti("requestId")) > 1 {
			return publicError(c, auth.ErrInvalid)
		}
		r, e := mobileCommandRequest(c, services, "status")
		if e != nil {
			return sessionError(c, e)
		}
		if id := c.Query("requestId"); id != "" {
			if len(id) > 128 {
				return publicError(c, auth.ErrInvalid)
			}
			r.CommandID = commands.IntentID("mobile", r.FamilyID, id)
		}
		return send(c, r)
	})
}
