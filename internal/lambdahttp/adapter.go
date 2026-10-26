// Package lambdahttp translates API Gateway HTTP API payload 2.0 to Fiber.
package lambdahttp

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

type contextKey struct{}
type metadataKey struct{}

// Metadata comes only from API Gateway's trusted requestContext.
type Metadata struct {
	SourceIP  string
	RequestID string
}

func RequestMetadata(ctx context.Context) Metadata {
	m, _ := ctx.Value(metadataKey{}).(Metadata)
	return m
}

type Adapter struct{ app *fiber.App }

// New must run before route registration so all routes receive the Lambda context.
func New(app *fiber.App) *Adapter {
	app.Use(func(c fiber.Ctx) error {
		ctx, ok := c.RequestCtx().UserValue(contextKey{}).(context.Context)
		if !ok {
			return fiber.ErrInternalServerError
		}
		c.SetContext(ctx)
		err := c.Next()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fiber.ErrGatewayTimeout
		}
		return err
	})
	return &Adapter{app: app}
}

func (a *Adapter) Handle(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	badRequest := events.APIGatewayV2HTTPResponse{StatusCode: 400, Body: "Bad Request", Headers: map[string]string{"content-type": "text/plain; charset=utf-8", "cache-control": "no-store"}}
	if event.Version != "2.0" || !strings.HasPrefix(event.RawPath, "/") || event.RequestContext.HTTP.Method == "" {
		return badRequest, nil
	}
	body := []byte(event.Body)
	if event.IsBase64Encoded {
		var err error
		body, err = base64.StdEncoding.DecodeString(event.Body)
		if err != nil {
			return badRequest, nil
		}
	}
	var request fasthttp.Request
	request.Header.SetMethod(event.RequestContext.HTTP.Method)
	for key, value := range event.Headers {
		// Forwarded values are supplied by the client, not an identity source.
		if strings.EqualFold(key, "X-Forwarded-For") || strings.EqualFold(key, "X-Real-IP") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		request.Header.Set(key, value)
	}
	request.Header.SetHost(event.RequestContext.DomainName)
	uri := event.RawPath
	if event.RawQueryString != "" {
		uri += "?" + event.RawQueryString
	}
	request.SetRequestURI(uri)
	if len(event.Cookies) > 0 {
		request.Header.Set("Cookie", strings.Join(event.Cookies, "; "))
	}
	request.SetBody(body)
	var fctx fasthttp.RequestCtx
	fctx.Init(&request, &net.TCPAddr{IP: net.ParseIP(event.RequestContext.HTTP.SourceIP)}, nil)
	ctx = context.WithValue(ctx, metadataKey{}, Metadata{SourceIP: event.RequestContext.HTTP.SourceIP, RequestID: event.RequestContext.RequestID})
	fctx.SetUserValue(contextKey{}, ctx)
	a.app.Handler()(&fctx)
	response := events.APIGatewayV2HTTPResponse{StatusCode: fctx.Response.StatusCode(), Headers: map[string]string{}}
	fctx.Response.Header.VisitAll(func(key, value []byte) {
		name := strings.ToLower(string(key))
		if name == "set-cookie" {
			response.Cookies = append(response.Cookies, string(value))
			return
		}
		if name == "content-length" {
			return
		}
		if existing, ok := response.Headers[name]; ok {
			response.Headers[name] = existing + ", " + string(value)
		} else {
			response.Headers[name] = string(value)
		}
	})
	responseBody := fctx.Response.Body()
	response.IsBase64Encoded = !utf8.Valid(responseBody)
	if response.IsBase64Encoded {
		response.Body = base64.StdEncoding.EncodeToString(responseBody)
	} else {
		response.Body = string(responseBody)
	}
	return response, nil
}
