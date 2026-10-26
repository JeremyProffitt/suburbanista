// seed consumes its one-use bootstrap only from the CI step environment.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/onstar"
	"github.com/JeremyProffittOrg/suburban-remote/internal/state"
)

type importer interface {
	Seed(context.Context, onstar.Bootstrap, int64, string) (bool, error)
}

func run(ctx context.Context, getenv func(string) string, newStore func(context.Context) (importer, error)) (string, error) {
	raw := getenv("ONSTAR_TOKEN_BOOTSTRAP")
	if raw == "" {
		return "seed: skipped, no bootstrap present", nil
	}
	if len(raw) > 48*1024 {
		return "", state.ErrInvalid
	}
	var bootstrap onstar.Bootstrap
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bootstrap) != nil {
		return "", state.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return "", state.ErrInvalid
	}
	version, e := strconv.ParseInt(getenv("ENROLLMENT_VERSION"), 10, 64)
	if e != nil || version <= 0 || bootstrap.EnrollmentVersion != version {
		return "", state.ErrInvalid
	}
	if bootstrap.ValidateDevice(getenv("ONSTAR_DEVICE_ID")) != nil {
		return "", state.ErrInvalid
	}
	store, e := newStore(ctx)
	if e != nil {
		return "", e
	}
	written, e := store.Seed(ctx, bootstrap, version, getenv("ONSTAR_DEVICE_ID"))
	if e != nil {
		return "", e
	}
	if !written {
		return "seed: skipped, enrollment already imported or superseded", nil
	}
	return "seed: encrypted enrollment imported", nil
}
func main() {
	if os.Getenv("ONSTAR_TOKEN_BOOTSTRAP") == "" {
		fmt.Println("seed: skipped, no bootstrap present")
		return
	}
	if !allowedCI(os.Getenv) {
		fmt.Fprintln(os.Stderr, "seed: import requires the repository main GitHub Actions deployment")
		os.Exit(1)
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		for _, name := range []string{"ONSTAR_TOKEN_BOOTSTRAP", "CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B", "ONSTAR_DEVICE_ID"} {
			mask(os.Stdout, os.Getenv(name))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	message, e := run(ctx, os.Getenv, func(ctx context.Context) (importer, error) { return state.NewFromEnvironment(ctx) })
	if e != nil {
		fmt.Fprintln(os.Stderr, "seed: failed; bootstrap retained for recovery")
		os.Exit(1)
	}
	fmt.Println(message)
}

func allowedCI(getenv func(string) string) bool {
	return getenv("GITHUB_ACTIONS") == "true" && getenv("GITHUB_REF") == "refs/heads/main" && getenv("GITHUB_REPOSITORY") == "JeremyProffittOrg/Suburban-Remote"
}

// GitHub workflow commands register masks before any operation or status output.
func mask(w io.Writer, value string) {
	if value == "" {
		return
	}
	quoted, _ := json.Marshal(value)
	for _, form := range []string{value, base64.StdEncoding.EncodeToString([]byte(value)), string(quoted[1 : len(quoted)-1]), url.QueryEscape(value)} {
		for _, line := range strings.Split(form, "\n") {
			if line != "" {
				fmt.Fprintf(w, "::add-mask::%s\n", strings.NewReplacer("%", "%25", "\r", "%0D").Replace(line))
			}
		}
	}
}
