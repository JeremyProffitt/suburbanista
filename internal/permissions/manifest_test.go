package permissions

import (
	"encoding/json"
	"os"
	"testing"
	"testing/fstest"
)

func TestManifests(t *testing.T) {
	for _, surface := range []string{"web", "mobile", "alexa"} {
		t.Run(surface, func(t *testing.T) {
			m, err := Load(os.DirFS("../.."), surface)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range m.Permissions {
				if entry.GrantSource == "application-pin" || entry.GrantSource == "alexa-managed-pin" {
					t.Fatal("owner-disabled PIN requirement was reintroduced")
				}
				if entry.Capability == "" || entry.Purpose == "" || entry.DeniedBehavior == "" || entry.GrantSource == "" || (entry.Requirement != "required" && entry.Requirement != "optional") {
					t.Fatal("incomplete grant")
				}
			}
			if surface == "mobile" {
				count := 0
				for _, entry := range m.Permissions {
					if entry.PlatformPermission != "" {
						count++
						if entry.PlatformPermission != "android.permission.INTERNET" && entry.PlatformPermission != "android.permission.POST_NOTIFICATIONS" && entry.PlatformPermission != "android.permission.ACCESS_COARSE_LOCATION" && entry.PlatformPermission != "android.permission.ACCESS_FINE_LOCATION" && entry.PlatformPermission != "android.permission.FOREGROUND_SERVICE" && entry.PlatformPermission != "android.permission.FOREGROUND_SERVICE_LOCATION" && entry.PlatformPermission != "android.permission.WAKE_LOCK" {
							t.Fatal("unused platform permission")
						}
					}
				}
				if count != 7 {
					t.Fatal("Android manifest permission set differs")
				}
			}
		})
	}
}

func TestRejectInvalidGrants(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*Manifest)
	}{
		{"missing", func(m *Manifest) { m.Permissions = m.Permissions[1:] }},
		{"duplicate", func(m *Manifest) { m.Permissions[1] = m.Permissions[0] }},
		{"purpose", func(m *Manifest) { m.Permissions[0].Purpose = " " }},
		{"denial", func(m *Manifest) { m.Permissions[0].DeniedBehavior = "" }},
		{"internet-runtime-prompt", func(m *Manifest) { m.Permissions[0].GrantSource = "android-runtime" }},
		{"phone-gps", func(m *Manifest) { m.Permissions[0].PlatformPermission = "android.permission.ACCESS_FINE_LOCATION" }},
		{"weather-fine-location", func(m *Manifest) { m.Permissions[3].PlatformPermission = "android.permission.ACCESS_FINE_LOCATION" }},
		{"weather-background-location", func(m *Manifest) {
			m.Permissions[3].PlatformPermission = "android.permission.ACCESS_BACKGROUND_LOCATION"
		}},
		{"weather-required", func(m *Manifest) { m.Permissions[3].Requirement = "required" }},
		{"weather-runtime-version", func(m *Manifest) { m.Permissions[3].RuntimeMinAPI = 22 }},
		{"broad-storage", func(m *Manifest) { m.Permissions[0].PlatformPermission = "android.permission.READ_EXTERNAL_STORAGE" }},
		{"installer", func(m *Manifest) { m.Permissions[5].PlatformPermission = "android.permission.REQUEST_INSTALL_PACKAGES" }},
		{"notification-required", func(m *Manifest) { m.Permissions[4].Requirement = "required" }},
		{"notification-version", func(m *Manifest) { m.Permissions[4].RuntimeMinAPI = 29 }},
		{"owner-sign-in-optional", func(m *Manifest) { m.Permissions[1].Requirement = "optional" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Load(os.DirFS("../.."), "mobile")
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&m)
			if err = m.Validate(); err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
}

func TestLoaderRejectsMalformedInput(t *testing.T) {
	m, err := Load(os.DirFS("../.."), "web")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	for _, body := range [][]byte{[]byte(`{`), append(append([]byte{}, data...), []byte(` {}`)...), append([]byte(`{"unexpected":true,`), data[1:]...)} {
		files := fstest.MapFS{"permissions/web.json": {Data: body}}
		if _, err = Load(files, "web"); err == nil {
			t.Fatal("malformed input accepted")
		}
	}
	if _, err = Load(fstest.MapFS{}, "../web"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err = Load(fstest.MapFS{}, "web"); err == nil {
		t.Fatal("missing manifest accepted")
	}
}
