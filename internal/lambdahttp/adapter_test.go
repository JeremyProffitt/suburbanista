package lambdahttp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/lambdahttp"
	"github.com/aws/aws-lambda-go/events"
	"github.com/gofiber/fiber/v3"
)

func fixture(t *testing.T) events.APIGatewayV2HTTPRequest {
	t.Helper()
	data, err := os.ReadFile("../web/testdata/http-api-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var event events.APIGatewayV2HTTPRequest
	if err = json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestLambdaHTTPContract(t *testing.T) {
	app := fiber.New()
	adapter := lambdahttp.New(app)
	app.Get("/contract", func(c fiber.Ctx) error {
		values := c.Request().URI().QueryArgs().PeekMulti("a")
		if len(values) != 2 || string(values[0]) != "1" || string(values[1]) != "2" || c.Query("b") != "x,y" {
			t.Error("query values were flattened or decoded incorrectly")
		}
		if c.Cookies("first") != "one" || c.Cookies("second") != "two" {
			t.Error("cookies missing")
		}
		m := lambdahttp.RequestMetadata(c.Context())
		if m.SourceIP != "192.0.2.10" || m.RequestID != "synthetic-request-id" || c.IP() != "192.0.2.10" {
			t.Error("trusted request metadata not preserved")
		}
		if c.Get("X-Forwarded-For") != "" || c.Get("X-Real-IP") != "" {
			t.Error("spoofed forwarding headers retained")
		}
		c.Response().Header.Add("Set-Cookie", "one=1; HttpOnly; Secure")
		c.Response().Header.Add("Set-Cookie", "two=2; HttpOnly; Secure")
		c.Response().Header.Add("X-Value", "first")
		c.Response().Header.Add("X-Value", "second")
		return c.SendString("ok")
	})
	app.Get("/redirect", func(c fiber.Ctx) error { c.Set("Location", "/privacy"); return c.SendStatus(302) })
	picture := image.NewRGBA(image.Rect(0, 0, 1, 1))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, picture); err != nil {
		t.Fatal(err)
	}
	app.Get("/binary", func(c fiber.Ctx) error { c.Set("Content-Type", "image/png"); return c.Send(pngBody.Bytes()) })
	app.Post("/body", func(c fiber.Ctx) error { return c.Send(c.Body()) })
	for _, tc := range []struct {
		name, path, method string
		status             int
	}{
		{"query-cookies-headers", "/contract", "GET", 200}, {"redirect", "/redirect", "GET", 302},
		{"binary", "/binary", "GET", 200}, {"not-found", "/absent", "GET", 404},
		{"method-not-allowed", "/contract", "POST", 405}, {"body", "/body", "POST", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := fixture(t)
			event.RawPath = tc.path
			event.RequestContext.HTTP.Method = tc.method
			if tc.name == "body" {
				event.Body = base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0x80})
				event.IsBase64Encoded = true
			}
			response, err := adapter.Handle(context.Background(), event)
			if err != nil || response.StatusCode != tc.status {
				t.Fatalf("status=%d wanted=%d err=%v", response.StatusCode, tc.status, err)
			}
			switch tc.name {
			case "query-cookies-headers":
				if !reflect.DeepEqual(response.Cookies, []string{"one=1; HttpOnly; Secure", "two=2; HttpOnly; Secure"}) {
					t.Fatalf("cookies: %v", response.Cookies)
				}
				if response.Headers["x-value"] != "first, second" {
					t.Fatalf("header values lost: %v", response.Headers)
				}
			case "redirect":
				if response.Headers["location"] != "/privacy" {
					t.Fatal("redirect location lost")
				}
			case "binary":
				body, err := base64.StdEncoding.DecodeString(response.Body)
				if !response.IsBase64Encoded || err != nil || !bytes.Equal(body, pngBody.Bytes()) {
					t.Fatal("PNG changed")
				}
			case "body":
				body, err := base64.StdEncoding.DecodeString(response.Body)
				if !response.IsBase64Encoded || err != nil || !bytes.Equal(body, []byte{0xff, 0, 0x80}) {
					t.Fatal("request binary changed")
				}
			}
		})
	}
	for _, kind := range []string{"version", "base64", "path"} {
		event := fixture(t)
		switch kind {
		case "version":
			event.Version = "1.0"
		case "base64":
			event.IsBase64Encoded = true
			event.Body = "%%%"
		case "path":
			event.RawPath = "https://untrusted.invalid/"
		}
		response, err := adapter.Handle(context.Background(), event)
		if err != nil || response.StatusCode != 400 {
			t.Fatalf("invalid %s accepted", kind)
		}
	}
}

func TestLambdaHTTPContractDeadline(t *testing.T) {
	app := fiber.New()
	adapter := lambdahttp.New(app)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	app.Get("/contract", func(c fiber.Ctx) error {
		actual, ok := c.Context().Deadline()
		if !ok || !actual.Equal(deadline) {
			t.Error("Lambda deadline not propagated")
		}
		<-c.Context().Done()
		return c.Context().Err()
	})
	start := time.Now()
	response, err := adapter.Handle(ctx, fixture(t))
	elapsed := time.Since(start)
	if err != nil || response.StatusCode != 504 || elapsed < 30*time.Millisecond || elapsed > time.Second {
		t.Fatalf("deadline response=%d elapsed=%s err=%v", response.StatusCode, elapsed, err)
	}
	if strings.Contains(response.Body, "context deadline") {
		t.Fatal("internal error exposed")
	}
}
