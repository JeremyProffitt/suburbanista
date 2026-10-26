package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/securelocal"
	synthetic "github.com/JeremyProffittOrg/suburban-remote/internal/testdata"
)

func TestEnrollmentRequiresAttendance(t *testing.T) {
	var output bytes.Buffer
	if run(nil, &output) == nil {
		t.Fatal("unattended enrollment accepted")
	}
	if run([]string{"--vin-stdin"}, &output) == nil {
		t.Fatal("piped VIN bypassed attendance")
	}
}

func TestPrivateVINPipe(t *testing.T) {
	value, err := readPipedVIN(strings.NewReader(synthetic.VIN + "\n"))
	if err != nil || strings.TrimSpace(string(value)) != synthetic.VIN {
		t.Fatal("private input failed")
	}
	clear(value)
	if value, err := readPipedVIN(strings.NewReader(strings.Repeat("x", 65))); err == nil || value != nil {
		t.Fatal("oversized input accepted")
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHTMLDenialBlocksNextAttemptButVehicle403DoesNot(t *testing.T) {
	fixture, err := os.ReadFile("../../internal/onstar/testdata/access-denied.html")
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	http.DefaultTransport = testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture))}, nil
	})
	address, authorization, err := onstar.NewAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	_, err = onstar.NewClient().Enroll(context.Background(), authorization, onstar.RedirectURI+"?state="+u.Query().Get("state")+"&code=synthetic")
	if err == nil {
		t.Fatal("HTML denial passed")
	}
	path := filepath.Join(t.TempDir(), "attempts.json")
	state := attempts{Attempts: 1}
	if e := saveAttempts(path, state); e != nil {
		t.Fatal(e)
	}
	_ = recordAuthFailure(path, state, err)
	loaded, e := loadAttempts(path)
	if e != nil || loaded.allowed(time.Now()) == nil {
		t.Fatal("subsequent attempt not blocked")
	}
	vehiclePath := filepath.Join(t.TempDir(), "attempts.json")
	if e := saveAttempts(vehiclePath, state); e != nil {
		t.Fatal(e)
	}
	http.DefaultTransport = testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("synthetic forbidden"))}, nil
	})
	_, err = onstar.NewClient().Vehicles(context.Background(), onstar.GMToken{AccessToken: "synthetic", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	_ = recordAuthFailure(vehiclePath, state, err)
	loaded, e = loadAttempts(vehiclePath)
	if e != nil || loaded.allowed(time.Now()) != nil || !loaded.CooldownUntil.IsZero() {
		t.Fatal("vehicle denial incorrectly triggered global auth cooldown")
	}
}

type fixtureExchanger struct {
	steps     *[]string
	linkError error
}

func (f fixtureExchanger) Refresh(_ context.Context, g onstar.Grant) (onstar.Grant, error) {
	*f.steps = append(*f.steps, "refresh")
	g.RefreshToken = "synthetic-rotated"
	return g, nil
}
func (f fixtureExchanger) Exchange(_ context.Context, g onstar.Grant, _, _ string) (onstar.GMToken, error) {
	*f.steps = append(*f.steps, "exchange/"+g.RefreshToken)
	return onstar.GMToken{}, f.linkError
}

func TestPersistencePrecedesRefreshAndLinkage(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		var steps []string
		writes := 0
		persist := func(g onstar.Grant, state string) error {
			writes++
			steps = append(steps, "persist/"+g.RefreshToken)
			if state != "pending-verification" {
				t.Fatal("wrong pre-probe state")
			}
			if writes == failAt {
				return errors.New("synthetic persistence failure")
			}
			return nil
		}
		_, _, err := persistAndExchange(context.Background(), fixtureExchanger{steps: &steps}, onstar.Grant{RefreshToken: "synthetic-original"}, synthetic.DeviceID, synthetic.VIN, persist)
		want := []string{"persist/synthetic-original", "refresh", "persist/synthetic-rotated", "exchange/synthetic-rotated"}
		if failAt == 1 {
			want = want[:1]
		}
		if failAt == 2 {
			want = want[:3]
		}
		if strings.Join(steps, ",") != strings.Join(want, ",") || (failAt > 0) != (err != nil) {
			t.Fatal("persistence ordering or fail-closed behavior differs")
		}
	}
	var steps []string
	var retained []string
	grant, _, err := persistAndExchange(context.Background(), fixtureExchanger{steps: &steps, linkError: &onstar.Error{Kind: "vehicle-not-linked"}}, onstar.Grant{RefreshToken: "synthetic-original"}, synthetic.DeviceID, synthetic.VIN, func(g onstar.Grant, _ string) error { retained = append(retained, g.RefreshToken); return nil })
	if err == nil || len(retained) != 2 || grant.RefreshToken != "synthetic-rotated" {
		t.Fatal("linkage failure discarded rotated grant")
	}
}

func TestEnrollmentVersionCannotResetAttemptBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.json")
	if err := saveAttempts(path, attempts{Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	version, err := nextVersion(func(args []string, _ []byte) ([]byte, error) {
		if args[1] == "list" {
			return []byte(`[{"name":"ENROLLMENT_VERSION"}]`), nil
		}
		return []byte("100"), nil
	})
	if err != nil || version != 101 {
		t.Fatal("version fixture failed")
	}
	state, err := loadAttempts(path)
	if err != nil || state.Attempts != 2 || state.allowed(time.Now()) == nil {
		t.Fatal("version change reset enrollment cap")
	}
}
func TestBootstrapUsesOnlyStdin(t *testing.T) {
	grant := onstar.Grant{RefreshToken: "synthetic-sensitive-refresh", AccessToken: "synthetic-sensitive-access", Subject: synthetic.Subject, RefreshObtainedAt: 1800000000}
	seed, err := onstar.NewBootstrap(grant, synthetic.DeviceID, synthetic.VIN, 1, "pending-verification")
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	run := func(args []string, input []byte) ([]byte, error) {
		joined := strings.Join(args, " ")
		for _, private := range []string{grant.RefreshToken, grant.AccessToken, synthetic.VIN, synthetic.DeviceID} {
			if strings.Contains(joined, private) {
				t.Fatal("private value in argv")
			}
		}
		if args[0] == "secret" && args[1] == "set" {
			writes++
			if !bytes.Contains(input, []byte(grant.RefreshToken)) || bytes.Contains(input, []byte(grant.AccessToken)) {
				t.Fatal("wrong bootstrap content")
			}
			return nil, nil
		}
		if args[0] == "variable" {
			return nil, nil
		}
		return []byte(`[{"name":"ONSTAR_TOKEN_BOOTSTRAP","updatedAt":"2026-10-22"}]`), nil
	}
	if err := installBootstrap(run, seed); err != nil || writes != 1 {
		t.Fatal("bootstrap install failed")
	}
	if seed.ValidateDevice(synthetic.DeviceID) != nil || seed.ValidateDevice("wrong-device") == nil {
		t.Fatal("device binding failed")
	}
	bad := func([]string, []byte) ([]byte, error) { return nil, errors.New("sensitive-diagnostic") }
	if err := installBootstrap(bad, seed); err == nil || strings.Contains(err.Error(), "sensitive-diagnostic") {
		t.Fatal("write error leaked")
	}
}
func TestAttemptCeilingAndCooldownPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.json")
	now := time.Now()
	state := attempts{Attempts: 1}
	if err := saveAttempts(path, state); err != nil {
		t.Fatal(err)
	}
	err := recordAuthFailure(path, state, &onstar.Error{Kind: "access-denied", Status: 403, AuthEndpoint: true})
	if err == nil {
		t.Fatal("failure lost")
	}
	loaded, err := loadAttempts(path)
	if err != nil || loaded.CooldownUntil.Before(now.Add(5*time.Hour+59*time.Minute)) || loaded.allowed(now) == nil {
		t.Fatal("cooldown not persisted")
	}
	loaded.Attempts = 2
	loaded.CooldownUntil = time.Time{}
	if err := saveAttempts(path, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadAttempts(path)
	if err != nil || loaded.allowed(now) == nil {
		t.Fatal("attempt ceiling lost")
	}
}
func TestEnrollmentLockExcludesConcurrentRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := lockEnrollment(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockEnrollment(path); err == nil {
		t.Fatal("concurrent enrollment accepted")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // Release is safe when called from multiple cleanup paths.
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock pathname was deleted or replaced")
	}
	release, err = lockEnrollment(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestDeviceRecoveryDecodesOnlyRequiredIdentity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI is Windows-only")
	}
	plain := []byte(`{"schema":1,"repository":"JeremyProffittOrg/Suburban-Remote","keys":{"ONSTAR_DEVICE_ID":"` + synthetic.DeviceID + `","CREDENTIAL_ENCRYPTION_KEY_A":{"ignored":"synthetic-unrelated-secret"},"UPDATE_MANIFEST_SIGNING_KEY":"synthetic-unrelated-private-key"}}`)
	sealed, err := securelocal.Protect(plain)
	clear(plain)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated-keys.dpapi"), sealed, 0600); err != nil {
		t.Fatal(err)
	}
	device, err := loadDevice(dir)
	if err != nil || device != synthetic.DeviceID {
		t.Fatal("device-only recovery failed")
	}
}
