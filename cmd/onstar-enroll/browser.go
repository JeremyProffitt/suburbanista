package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/mxschmitt/playwright-go"
)

func privateBrowserOptions() (*playwright.RunOptions, error) {
	// DEBUGP writes raw protocol traffic directly to stdout in the Go binding.
	for _, name := range []string{"DEBUGP", "DEBUG", "PWDEBUG", "NODE_OPTIONS", "NODE_DEBUG", "NODE_DEBUG_NATIVE", "SSLKEYLOGFILE", "CHROME_LOG_FILE"} {
		if os.Unsetenv(name) != nil {
			return nil, errors.New("cannot disable browser diagnostics")
		}
	}
	return &playwright.RunOptions{Browsers: []string{"chromium"}, NoInstallShell: true, Verbose: false, Stdout: io.Discard, Stderr: io.Discard, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, nil
}

func installEnrollmentBrowser() error {
	opts, err := privateBrowserOptions()
	if err != nil {
		return err
	}
	if playwright.Install(opts) != nil {
		return errors.New("Playwright browser installation failed; no enrollment attempted")
	}
	return nil
}

type enrollmentBrowser struct {
	driver    *playwright.Playwright
	browser   playwright.Browser
	page      playwright.Page
	session   playwright.CDPSession
	closeOnce sync.Once
}

func openEnrollmentBrowser(headless bool) (*enrollmentBrowser, error) {
	opts, err := privateBrowserOptions()
	if err != nil {
		return nil, err
	}
	pw, err := playwright.Run(opts)
	if err != nil {
		return nil, errors.New("Playwright is not ready; run onstar-enroll.bat --install-browser first")
	}
	b := &enrollmentBrowser{driver: pw}
	b.browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(headless), ChromiumSandbox: playwright.Bool(true), Timeout: playwright.Float(30000)})
	if err != nil {
		b.Close()
		return nil, errors.New("cannot open Playwright browser; no enrollment attempted")
	}
	bc, err := b.browser.NewContext(playwright.BrowserNewContextOptions{AcceptDownloads: playwright.Bool(false)})
	if err != nil {
		b.Close()
		return nil, errors.New("cannot create private browser session")
	}
	b.page, err = bc.NewPage()
	if err == nil {
		b.session, err = bc.NewCDPSession(b.page)
	}
	if err != nil {
		b.Close()
		return nil, errors.New("cannot prepare callback capture")
	}
	return b, nil
}

func (b *enrollmentBrowser) Close() {
	b.closeOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if b.browser != nil {
				_ = b.browser.Close()
			}
			_ = b.driver.Stop()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			killBrowserDriver(b.driver.Pid())
		}
	})
}

// callbackLocation accepts only this authorization's registered redirect. Neither
// URLs, page bodies, credentials nor Playwright errors are returned in diagnostics.
func callbackLocation(location, expectedState string) (string, error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", errors.New("invalid browser callback")
	}
	if !strings.EqualFold(u.Scheme, "msauth.com.gm.myChevrolet") {
		return "", nil
	}
	q, err := url.ParseQuery(u.RawQuery)
	var reasons []string
	if err != nil {
		reasons = append(reasons, "malformed-query")
	}
	if u.Host != "auth" {
		reasons = append(reasons, "wrong-host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		reasons = append(reasons, "unexpected-path")
	}
	if u.User != nil {
		reasons = append(reasons, "unexpected-userinfo")
	}
	if u.Fragment != "" {
		reasons = append(reasons, "unexpected-fragment")
	}
	if len(q["state"]) != 1 {
		reasons = append(reasons, "missing-or-duplicate-state")
	} else if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(expectedState)) != 1 {
		reasons = append(reasons, "state-mismatch")
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		reasons = append(reasons, "missing-or-duplicate-code")
	} else if len(q.Get("code")) > 4096 {
		reasons = append(reasons, "code-exceeds-size-limit")
	}
	if q.Get("error") != "" {
		label := "provider-error"
		switch q.Get("error") {
		case "access_denied", "invalid_request", "invalid_scope", "unauthorized_client", "server_error", "temporarily_unavailable":
			label = q.Get("error")
		}
		reasons = append(reasons, label)
		// Only a fixed-format public B2C error identifier may leave the process.
		// Never echo error_description, which can include account information.
		if code := b2cErrorID.FindString(q.Get("error_description")); code != "" {
			reasons = append(reasons, code)
		}
	}
	if len(reasons) > 0 {
		return "", errors.New("browser callback rejected: " + strings.Join(reasons, ", "))
	}
	return location, nil
}

var b2cErrorID = regexp.MustCompile(`\bAADB2C[0-9]{5,10}\b`)

type browserResult struct {
	redirect string
	err      error
}

func (b *enrollmentBrowser) Capture(ctx context.Context, address, origin string) ([]byte, error) {
	start, err := url.Parse(address)
	if err != nil || start.Scheme+"://"+start.Host != origin || start.Query().Get("state") == "" {
		return nil, errors.New("invalid enrollment start address")
	}
	expectedState := start.Query().Get("state")
	results := make(chan browserResult, 1)
	var once sync.Once
	finish := func(r browserResult) { once.Do(func() { results <- r }) }
	denied := func() {
		finish(browserResult{err: &onstar.Error{Kind: "access-denied", Status: 403, AuthEndpoint: true}})
	}
	b.page.OnClose(func(playwright.Page) { finish(browserResult{err: errors.New("browser closed; enrollment cancelled")}) })
	b.browser.OnDisconnected(func(playwright.Browser) {
		finish(browserResult{err: errors.New("browser disconnected; enrollment cancelled")})
	})
	b.page.OnDOMContentLoaded(func(playwright.Page) {
		go func() {
			title, e := b.page.Title()
			if e == nil && strings.EqualFold(strings.TrimSpace(title), "Access Denied") {
				denied()
			}
		}()
	})
	b.session.On("Fetch.requestPaused", func(event map[string]any) {
		go func() {
			id, _ := event["requestId"].(string)
			if id == "" {
				finish(browserResult{err: errors.New("invalid browser interception event")})
				return
			}
			request, _ := event["request"].(map[string]any)
			requestURL, _ := request["url"].(string)
			u, e := url.Parse(requestURL)
			if e != nil || u.Scheme+"://"+u.Host != origin {
				finish(browserResult{err: errors.New("untrusted browser response")})
				return
			}
			status, _ := event["responseStatusCode"].(float64)
			location := ""
			if headers, ok := event["responseHeaders"].([]any); ok {
				for _, value := range headers {
					h, _ := value.(map[string]any)
					name, _ := h["name"].(string)
					if strings.EqualFold(name, "location") {
						location, _ = h["value"].(string)
					}
				}
			}
			if status >= 300 && status < 400 && location != "" {
				redirect, e := callbackLocation(location, expectedState)
				if e != nil || redirect != "" {
					// Fulfill the intercepted response instead of following the custom
					// protocol into an OS application. Never refetch a login request.
					_, captureErr := b.session.Send("Fetch.fulfillRequest", map[string]any{"requestId": id, "responseCode": 200, "body": base64.StdEncoding.EncodeToString([]byte("Enrollment callback captured. You may close this window."))})
					if captureErr != nil {
						e = errors.New("cannot finish private callback capture")
					}
					finish(browserResult{redirect: redirect, err: e})
					return
				}
			}
			resource, _ := event["resourceType"].(string)
			if (status == 401 || status == 403) && (resource == "Document" || strings.Contains(strings.ToLower(u.Path), "/api/")) {
				denied()
			}
			if _, e := b.session.Send("Fetch.continueRequest", map[string]any{"requestId": id}); e != nil {
				finish(browserResult{err: errors.New("browser navigation interrupted")})
			}
		}()
	})
	if _, err = b.session.Send("Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": origin + "/*", "requestStage": "Response"}}}); err != nil {
		return nil, errors.New("cannot enable private callback capture")
	}
	go func() {
		_, e := b.page.Goto(address, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded, Timeout: playwright.Float(30000)})
		if e != nil {
			finish(browserResult{err: errors.New("GM sign-in page did not load; no automatic retry")})
		}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			return nil, result.err
		}
		return []byte(result.redirect), nil
	case <-ctx.Done():
		return nil, errors.New("browser enrollment cancelled or timed out")
	}
}
