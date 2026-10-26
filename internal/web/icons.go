package web

import (
	"embed"

	"github.com/gofiber/fiber/v3"
)

// Versioned filenames are immutable: new artwork must use a new version.
//
//go:embed static/suburban-icon-v1-108.png static/suburban-icon-v1-512.png static/suburban-silver-v1-wide.png static/suburbanista-icon-v2-108.png static/suburbanista-icon-v2-512.png
var iconAssets embed.FS

func registerIcons(app *fiber.App) {
	for _, name := range []string{"suburban-icon-v1-108.png", "suburban-icon-v1-512.png", "suburban-silver-v1-wide.png", "suburbanista-icon-v2-108.png", "suburbanista-icon-v2-512.png"} {
		data, err := iconAssets.ReadFile("static/" + name)
		if err != nil {
			panic("embedded icon unavailable")
		}
		app.Get("/assets/"+name, func(c fiber.Ctx) error {
			c.Set("Content-Type", "image/png")
			c.Set("Cache-Control", "public, max-age=31536000, immutable")
			c.Set("X-Content-Type-Options", "nosniff")
			return c.Send(data)
		})
	}
}
