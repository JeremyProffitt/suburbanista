// Package permissions loads the declared consent contract for each client.
// A manifest describes required grants; it is not evidence that any was granted.
package permissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"
)

type Entry struct {
	Capability         string `json:"capability"`
	Purpose            string `json:"purpose"`
	Requirement        string `json:"requirement"`
	GrantSource        string `json:"grantSource"`
	DeniedBehavior     string `json:"deniedBehavior"`
	PlatformPermission string `json:"platformPermission,omitempty"`
	RuntimeMinAPI      int    `json:"runtimeMinAPI,omitempty"`
}

type Manifest struct {
	SchemaVersion string  `json:"schemaVersion"`
	Surface       string  `json:"surface"`
	Permissions   []Entry `json:"permissions"`
}

type rule struct {
	source, requirement, permission string
	runtimeMinAPI                   int
}

func rules(surface string) map[string]rule {
	common := map[string]rule{
		"owner-sign-in":               {source: "amazon-login", requirement: "required"},
		"vehicle-control":             {source: "application-consent", requirement: "required"},
		"background-vehicle-tracking": {source: "application-consent", requirement: "optional"},
	}
	switch surface {
	case "web":
	case "mobile":
		common["network-access"] = rule{"android-manifest", "required", "android.permission.INTERNET", 0}
		common["update-notifications"] = rule{"android-runtime", "optional", "android.permission.POST_NOTIFICATIONS", 33}
		common["update-installation"] = rule{source: "system-browser-installer", requirement: "optional"}
		common["phone-weather-location"] = rule{"android-runtime", "optional", "android.permission.ACCESS_COARSE_LOCATION", 23}
		common["phone-tracking-location"] = rule{"android-runtime", "optional", "android.permission.ACCESS_FINE_LOCATION", 23}
		common["phone-tracking-service"] = rule{"android-manifest", "optional", "android.permission.FOREGROUND_SERVICE", 0}
		common["phone-tracking-service-type"] = rule{"android-manifest", "optional", "android.permission.FOREGROUND_SERVICE_LOCATION", 0}
		common["phone-upload-wake"] = rule{"android-manifest", "optional", "android.permission.WAKE_LOCK", 0}
	case "alexa":
		common["owner-sign-in"] = rule{source: "alexa-account-linking", requirement: "required"}
		common["person-identification"] = rule{source: "alexa-personalization", requirement: "required"}
	default:
		return nil
	}
	return common
}

// Validate rejects missing capabilities, duplicate grants, unsupported platform
// permissions and invented runtime prompts. Scheduling permissions are not added
// until the Android feasibility proof establishes an actual requirement.
func (m Manifest) Validate() error {
	expected := rules(m.Surface)
	if m.SchemaVersion != "1" || expected == nil || len(m.Permissions) != len(expected) {
		return errors.New("invalid permission manifest header or capability count")
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range m.Permissions {
		r, ok := expected[entry.Capability]
		if !ok || seen[entry.Capability] {
			return errors.New("unknown or duplicate permission capability")
		}
		seen[entry.Capability] = true
		if strings.TrimSpace(entry.Purpose) == "" || strings.TrimSpace(entry.DeniedBehavior) == "" {
			return errors.New("permission purpose and denial behavior are required")
		}
		if entry.GrantSource != r.source || entry.Requirement != r.requirement || entry.PlatformPermission != r.permission || entry.RuntimeMinAPI != r.runtimeMinAPI {
			return errors.New("permission grant contract differs from supported platform behavior")
		}
	}
	return nil
}

// Load reads permissions/<surface>.json from the supplied repository or embedded
// filesystem. Surface is restricted before constructing the path.
func Load(files fs.FS, surface string) (Manifest, error) {
	if rules(surface) == nil {
		return Manifest{}, errors.New("unsupported permission surface")
	}
	file, err := files.Open("permissions/" + surface + ".json")
	if err != nil {
		return Manifest{}, errors.New("permission manifest unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return Manifest{}, errors.New("permission manifest unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var m Manifest
	if err = decoder.Decode(&m); err != nil {
		return Manifest{}, errors.New("invalid permission manifest JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Manifest{}, errors.New("extra permission manifest JSON")
	}
	if m.Surface != surface {
		return Manifest{}, errors.New("permission manifest surface mismatch")
	}
	if err = m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
