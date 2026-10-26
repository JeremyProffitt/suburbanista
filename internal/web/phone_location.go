package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/locationmap"
	"github.com/gofiber/fiber/v3"
)

type PhoneLocationWriter func(context.Context, string, locationmap.PhoneSample) (locationmap.PhoneResult, error)

func registerPhoneLocation(app *fiber.App, services Services) {
	app.Post("/api/location/phone", func(c fiber.Ctx) error {
		c.Set("Cache-Control", "private, no-store")
		if services.Mobile == nil {
			return sessionError(c, auth.ErrHandle)
		}
		family, err := services.Mobile.Authorize(c.Context(), bearer(c))
		if err != nil {
			return sessionError(c, err)
		}
		if len(c.Body()) > 1024 || len(c.Request().URI().QueryString()) != 0 {
			return publicError(c, auth.ErrInvalid)
		}
		var sample *locationmap.PhoneSample
		decoder := json.NewDecoder(bytes.NewReader(c.Body()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&sample) != nil || sample == nil || decoder.Decode(new(any)) != io.EOF {
			return publicError(c, auth.ErrInvalid)
		}
		if services.PhoneLocations == nil {
			return publicError(c, auth.ErrUnavailable)
		}
		result, err := services.PhoneLocations(c.Context(), family, *sample)
		if err != nil {
			return publicError(c, err)
		}
		return c.JSON(result)
	})
}
