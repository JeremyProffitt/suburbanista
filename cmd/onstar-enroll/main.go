// Command onstar-enroll uses attended Playwright callback capture.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/securelocal"
)

const repository = "JeremyProffittOrg/Suburban-Remote"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("onstar-enroll", flag.ContinueOnError)
	flags.SetOutput(out)
	attended := flags.Bool("attended", false, "owner is present and will sign in manually")
	installBrowser := flags.Bool("install-browser", false, "install the pinned Playwright Chromium runtime without enrollment")
	vinStdin := flags.Bool("vin-stdin", false, "receive only the VIN through a private stdin pipe; browser sign-in remains attended")
	home, _ := os.UserHomeDir()
	recovery := flags.String("recovery-dir", filepath.Join(home, ".suburban-remote", "recovery"), "existing protected recovery directory")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid enrollment options")
	}
	if *installBrowser && flags.NArg() == 0 {
		fmt.Fprintln(out, "Installing the pinned Playwright browser; no GM requests or credentials.")
		if err := installEnrollmentBrowser(); err != nil {
			return err
		}
		fmt.Fprintln(out, "Playwright browser ready.")
		return nil
	}
	if !*attended || flags.NArg() != 0 {
		return errors.New("run onstar-enroll --attended from an owner-attended Windows console")
	}
	if !*vinStdin {
		if err := checkConsole(); err != nil {
			return err
		}
	}
	deviceID, err := loadDevice(*recovery)
	if err != nil {
		return err
	}
	statePath := filepath.Join(*recovery, "onstar-enrollment-attempts.json")
	unlock, err := lockEnrollment(statePath + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	state, err := loadAttempts(statePath)
	if err != nil {
		return err
	}
	if err := state.allowed(time.Now()); err != nil {
		return err
	}
	version, err := nextVersion(runGH)
	if err != nil {
		return err
	}
	var vinBytes []byte
	if *vinStdin {
		vinBytes, err = readPipedVIN(os.Stdin)
	} else {
		fmt.Fprintln(out, "Enter the vehicle VIN privately (hidden):")
		vinBytes, err = readHidden()
	}
	if err != nil {
		return err
	}
	vin := strings.ToUpper(strings.TrimSpace(string(vinBytes)))
	clear(vinBytes)
	// Validate without creating a grant or making provider calls.
	if !regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`).MatchString(vin) {
		return errors.New("invalid VIN; no provider request sent")
	}
	fmt.Fprintln(out, "Use the owner GM account already confirmed during setup, with this vehicle in its myChevrolet garage and OnStar active.")
	attendedContext, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	browser, err := openEnrollmentBrowser(false)
	if err != nil {
		return err
	}
	defer browser.Close()
	if attendedContext.Err() != nil {
		return errors.New("enrollment cancelled before sign-in")
	}
	state.Attempts++
	if err := saveAttempts(statePath, state); err != nil {
		return err
	}
	address, authorization, err := onstar.NewAuthorization()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Sign in and complete MFA yourself in the Playwright window. The helper captures the callback automatically; no URL copying is needed. Close the window or press Ctrl+C to cancel.")
	captureContext, cancelCapture := context.WithTimeout(attendedContext, 9*time.Minute+50*time.Second)
	redirect, err := browser.Capture(captureContext, address, "https://custlogin.gm.com")
	cancelCapture()
	browser.Close()
	if err != nil {
		return recordAuthFailure(statePath, state, err)
	}
	defer clear(redirect)
	fmt.Fprintln(out, "Callback captured privately; verifying and storing the grant.")
	client := onstar.NewClient()
	ctx, cancel := context.WithTimeout(attendedContext, 3*time.Minute)
	defer cancel()
	grant, err := client.Enroll(ctx, authorization, string(redirect))
	if err != nil {
		return recordAuthFailure(statePath, state, err)
	}
	// Save the signed B2C grant before any linkage probe. Failed linkage never
	// deletes the grant; a rotated refresh token is saved before subsequent work.
	install := func(g onstar.Grant, providerState string) error {
		b, e := onstar.NewBootstrap(g, deviceID, vin, version, providerState)
		if e != nil {
			return e
		}
		return installBootstrap(runGH, b)
	}
	grant, token, err := persistAndExchange(ctx, client, grant, deviceID, vin, install)
	if err != nil {
		var provider *onstar.Error
		if errors.As(err, &provider) && provider.Kind == "vehicle-not-linked" {
			if e := install(grant, "vehicle-not-linked"); e != nil {
				return e
			}
			return errors.New("partial enrollment: grant retained; vehicle-not-linked. Check the vehicle in the myChevrolet garage")
		}
		return recordAuthFailure(statePath, state, err)
	}
	vehicles, err := client.Vehicles(ctx, token)
	if err != nil {
		return errors.New("partial enrollment: grant retained; garage read failed")
	}
	found := false
	for _, vehicle := range vehicles {
		if vehicle.VIN == vin {
			found = true
			break
		}
	}
	if !found {
		if err := install(grant, "vehicle-not-linked"); err != nil {
			return err
		}
		return errors.New("partial enrollment: grant retained; enrolled vehicle absent from garage")
	}
	location, err := client.Location(ctx, token, vin, false)
	if err != nil {
		return errors.New("partial enrollment: grant retained; cached location-status read failed")
	}
	if err := install(grant, "ready"); err != nil {
		return err
	}
	fmt.Fprintf(out, "Enrollment seed installed; local refresh and read-only vehicle checks passed. Location pending: %t. Fix coordinates remain unavailable until schema proof.\n", location.Pending)
	fmt.Fprintln(out, "This is not deployed readiness: two Lambda cold starts and actual location field verification remain required.")
	return nil
}

func readPipedVIN(input io.Reader) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(input, 65))
	if err != nil || len(value) > 64 {
		clear(value)
		return nil, errors.New("invalid private VIN input")
	}
	return value, nil
}

type grantExchanger interface {
	Refresh(context.Context, onstar.Grant) (onstar.Grant, error)
	Exchange(context.Context, onstar.Grant, string, string) (onstar.GMToken, error)
}

// Persist both the original and any rotated grant before the next provider
// operation. This seam tests the real enrollment ordering without live calls.
func persistAndExchange(ctx context.Context, client grantExchanger, grant onstar.Grant, deviceID, vin string, install func(onstar.Grant, string) error) (onstar.Grant, onstar.GMToken, error) {
	if err := install(grant, "pending-verification"); err != nil {
		return grant, onstar.GMToken{}, err
	}
	refreshed, err := client.Refresh(ctx, grant)
	if err != nil {
		return grant, onstar.GMToken{}, fmt.Errorf("partial enrollment: grant retained; %w", err)
	}
	grant = refreshed
	if err := install(grant, "pending-verification"); err != nil {
		return grant, onstar.GMToken{}, fmt.Errorf("refresh persistence failed; no GM request sent: %w", err)
	}
	token, err := client.Exchange(ctx, grant, deviceID, vin)
	if err != nil {
		return grant, token, fmt.Errorf("partial enrollment: grant retained; %w", err)
	}
	return grant, token, nil
}

func loadDevice(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "generated-keys.dpapi"))
	if err != nil {
		return "", errors.New("original protected device identity is unavailable; restore generated-keys.dpapi")
	}
	plain, err := securelocal.Unprotect(data)
	if err != nil {
		return "", errors.New("original device identity cannot be opened by this Windows user")
	}
	defer clear(plain)
	var recovery struct {
		Schema     int    `json:"schema"`
		Repository string `json:"repository"`
		Keys       struct {
			DeviceID string `json:"ONSTAR_DEVICE_ID"`
		} `json:"keys"`
	}
	if json.Unmarshal(plain, &recovery) != nil || recovery.Schema != 1 || recovery.Repository != repository || len(recovery.Keys.DeviceID) != 36 {
		return "", errors.New("invalid protected device recovery")
	}
	return recovery.Keys.DeviceID, nil
}

type command func([]string, []byte) ([]byte, error)

func runGH(args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	hideProcess(cmd)
	cmd.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("GitHub operation failed; no provider details were logged")
	}
	return output.Bytes(), nil
}
func nextVersion(run command) (int64, error) {
	data, err := run([]string{"variable", "list", "--repo", repository, "--json", "name"}, nil)
	if err != nil {
		return 0, err
	}
	var variables []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &variables) != nil {
		return 0, errors.New("invalid GitHub variable inventory")
	}
	for _, v := range variables {
		if v.Name == "ENROLLMENT_VERSION" {
			raw, err := run([]string{"variable", "get", "ENROLLMENT_VERSION", "--repo", repository}, nil)
			if err != nil {
				return 0, err
			}
			version, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil || version < 1 || version >= 1<<62 {
				return 0, errors.New("invalid enrollment version")
			}
			return version + 1, nil
		}
	}
	return 1, nil
}
func installBootstrap(run command, b onstar.Bootstrap) error {
	data, err := json.Marshal(b)
	if err != nil {
		return errors.New("cannot encode enrollment seed")
	}
	defer clear(data)
	if len(data) > 48*1024 {
		return errors.New("enrollment seed exceeds GitHub secret limit")
	}
	if _, err := run([]string{"secret", "set", "ONSTAR_TOKEN_BOOTSTRAP", "--repo", repository}, data); err != nil {
		return errors.New("enrollment seed write failed; no token was logged")
	}
	if _, err := run([]string{"variable", "set", "ENROLLMENT_VERSION", "--repo", repository}, []byte(strconv.FormatInt(b.EnrollmentVersion, 10))); err != nil {
		return errors.New("enrollment version write failed after seed installation")
	}
	metadata, err := run([]string{"secret", "list", "--repo", repository, "--json", "name,updatedAt"}, nil)
	if err != nil {
		return err
	}
	var entries []struct {
		Name    string `json:"name"`
		Updated string `json:"updatedAt"`
	}
	if json.Unmarshal(metadata, &entries) != nil {
		return errors.New("invalid secret metadata")
	}
	for _, entry := range entries {
		if entry.Name == "ONSTAR_TOKEN_BOOTSTRAP" && entry.Updated != "" {
			return nil
		}
	}
	return errors.New("enrollment seed metadata verification failed")
}
