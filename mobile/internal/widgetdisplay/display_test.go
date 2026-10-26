package widgetdisplay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAtomicRoundTripAndClockAge(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-2 * time.Minute)
	expiry := now.Add(30 * time.Minute)
	s := Snapshot{SchemaVersion: 1, GeneratedAt: now, Weather: Reading{Text: "75°", Available: true, ObservedAt: &observed, ExpiresAt: &expiry}, Range: Reading{Text: "310 mi", Available: true, ObservedAt: &observed, Freshness: "stale"}, Fuel: Reading{Text: "62%", Available: true}}
	path := filepath.Join(t.TempDir(), "widget-display.json")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	first := loaded.Display(now)
	if loaded.Range.Freshness != "stale" || strings.Contains(first.Range, "stale") {
		t.Fatal("freshness metadata was lost or exposed as a stale label")
	}
	later := loaded.Display(now.Add(31 * time.Minute))
	if first.Range != later.Range || !strings.Contains(later.Range, "11:58 AM") || strings.Contains(later.Range, "UTC") || strings.Contains(later.Range, "ago") || later.Weather != "Weather unavailable" || later.Fuel != "62% · age unknown" {
		t.Fatalf("frozen or invented time: %#v", later)
	}
	s.Range = Reading{}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.Display(now).Updated != "Source time unknown" {
		t.Fatal("weather/generated time became vehicle observation")
	}
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("partial file accepted")
	}
	if _, err = Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing snapshot invented data")
	}
}
