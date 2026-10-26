package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/internal/commands"
	"github.com/JeremyProffittOrg/suburban-remote/internal/lambdahttp"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
	"github.com/gofiber/fiber/v3"
)

type Build struct {
	Version string `json:"version"`
	Built   string `json:"built"`
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Services struct {
	Mobile           *auth.MobileService
	Provider         *state.Store
	Flags            commands.FlagReader
	Commands         CommandBroker
	Releases         ReleaseStore
	Map              MapStore
	LocationRequests LocationRequests
	PhoneLocations   PhoneLocationWriter
	MapKeySHA256     string
}

func New(build Build, services ...Services) (*lambdahttp.Adapter, error) {
	built, err := time.Parse(time.RFC3339, build.Built)
	if !commitPattern.MatchString(build.Version) || err != nil || built.Location() != time.UTC {
		return nil, errors.New("invalid build metadata")
	}
	health, err := json.Marshal(build)
	if err != nil {
		return nil, err
	}
	app := fiber.New()
	adapter := lambdahttp.New(app)
	app.Use(func(c fiber.Ctx) error { c.Set("Cache-Control", "no-store"); return c.Next() })
	registerIcons(app)
	app.Get("/healthz", func(c fiber.Ctx) error { c.Set("Content-Type", "application/json"); return c.Send(health) })
	app.Get("/privacy", func(c fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Suburbanista privacy</title><main><h1>Privacy</h1><p>Amazon sign-in protects one owner. Sessions are revocable; Android storage protects credentials. Location history is encrypted for 90 days. Logs retain seven days without credentials or locations. Vehicle polls run every 30 minutes. Authorized phone tracking records movement over 50 feet at most every two minutes. A synchronized phone can supply map position; Home/Work use configured centers. Raw observations remain intact. Only map requests generate missing imagery; private backgrounds are reused within 50 feet for up to 30 days. Viewing never wakes the vehicle. Map keys read images only; coordinates require an owner session. Preview is synthetic. Signed APK updates use private owner links. <a href="https://docs.aws.amazon.com/location/latest/developerguide/data-attribution.html">Map attribution</a>.</p></main></html>`)
	})
	if len(services) > 0 {
		if len(services) != 1 || services[0].Mobile == nil || services[0].Provider == nil {
			return nil, errors.New("invalid API service configuration")
		}
		registerMobile(app, services[0])
		registerPhoneLocation(app, services[0])
		return adapter, nil
	}
	app.Get("/auth/lwa/callback", func(c fiber.Ctx) error {
		states := c.Request().URI().QueryArgs().PeekMulti("state")
		if len(states) != 1 {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid login state")
		}
		state, err := base64.RawURLEncoding.Strict().DecodeString(string(states[0]))
		if err != nil || len(state) != 32 {
			return c.Status(fiber.StatusBadRequest).SendString("Invalid login state")
		}
		// The baseline cannot issue state, so even well-formed input is unrecognized.
		return c.Status(fiber.StatusBadRequest).SendString("Unrecognized login state")
	})
	return adapter, nil
}
