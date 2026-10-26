package web

import (
	"context"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/gofiber/fiber/v3"
)

func registerFeatures(app *fiber.App, services Services) {
	invoke := func(c fiber.Ctx, operation, featureID, requestID string, trace bool) (contract.AlexaCommandResponse, error) {
		r, err := mobileCommandRequest(c, services, operation)
		if err != nil {
			return contract.AlexaCommandResponse{}, err
		}
		if services.Commands == nil {
			return contract.AlexaCommandResponse{}, auth.ErrUnavailable
		}
		r.FeatureID = featureID
		r.ReadTrace = trace
		if requestID != "" {
			r.RequestID = requestID
		}
		ctx, cancel := context.WithTimeout(c.Context(), 4500*time.Millisecond)
		defer cancel()
		out, err := services.Commands.Command(ctx, r)
		if err != nil {
			return out, auth.ErrUnavailable
		}
		return out, nil
	}
	app.Get("/api/features", func(c fiber.Ctx) error {
		out, err := invoke(c, "features", "", "", false)
		if err != nil {
			return sessionError(c, err)
		}
		if out.State == "rejected" || out.State == "unconfirmed" {
			return c.Status(503).JSON(out)
		}
		return c.JSON(contract.FeaturesResponse{Features: contract.FeatureCatalog(), Availability: out.Availability, FetchedAt: time.Now().UTC()})
	})
	read := func(c fiber.Ctx, operation, featureID, requestID string, trace bool) error {
		feature, ok := contract.FindFeature(featureID)
		if !ok || feature.Category != "read" || len(requestID) > 128 {
			return publicError(c, auth.ErrInvalid)
		}
		out, err := invoke(c, operation, featureID, requestID, trace)
		if err != nil {
			return sessionError(c, err)
		}
		status := 200
		if out.State == "unconfirmed" {
			status = 503
		}
		if out.State == "rejected" {
			status = 403
		}
		return c.Status(status).JSON(out)
	}
	app.Post("/api/vehicle/read", func(c fiber.Ctx) error {
		var body struct {
			FeatureID string `json:"featureId"`
			RequestID string `json:"requestId,omitempty"`
			Trace     bool   `json:"trace,omitempty"`
		}
		if err := input(c, &body); err != nil {
			return publicError(c, err)
		}
		return read(c, "read", body.FeatureID, body.RequestID, body.Trace)
	})
	app.Get("/api/vehicle/read/status", func(c fiber.Ctx) error {
		args := c.Request().URI().QueryArgs()
		if len(args.PeekMulti("featureId")) != 1 || len(args.PeekMulti("requestId")) > 1 {
			return publicError(c, auth.ErrInvalid)
		}
		return read(c, "cached-read", c.Query("featureId"), c.Query("requestId"), false)
	})
}
