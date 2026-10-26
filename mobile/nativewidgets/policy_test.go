package nativewidgets

import (
	"encoding/xml"
	"io"
	"os"
	"strings"
	"testing"
)

func TestLocationFixAcknowledgmentHasNoWidgetWork(t *testing.T) {
	if !locationFixAcknowledgment(locationFixReadyAction) {
		t.Fatal("private location acknowledgment missing")
	}
	if acceptedAction(locationFixReadyAction) || needsNetwork(locationFixReadyAction) {
		t.Fatal("location acknowledgment reached widget work")
	}
	if _, ok := commandFor(locationFixReadyAction); ok {
		t.Fatal("location acknowledgment reached vehicle commands")
	}
	for _, other := range []string{actionPrefix + "START", actionPrefix + "SYNC", locationFixReadyAction + "_START"} {
		if locationFixAcknowledgment(other) {
			t.Fatal("unrelated action acknowledged as location")
		}
	}
}

func TestNativeLayoutsContainOnlySharedPixelsAndExactHitWrappers(t *testing.T) {
	for _, kind := range []string{"compact", "full"} {
		data, err := os.ReadFile("res/layout/suburban_widget_" + kind + ".xml")
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(strings.NewReader(string(data)))
		ids := map[string]string{}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			if start.Name.Local != "FrameLayout" && start.Name.Local != "ImageView" {
				t.Fatal("native layout replaced shared pixels with native styling")
			}
			for _, a := range start.Attr {
				if a.Name.Local == "id" {
					if ids[a.Value] != "" {
						t.Fatal("duplicate native widget id")
					}
					ids[a.Value] = start.Name.Local
				}
			}
		}
		if ids["@+id/widget_canvas"] != "FrameLayout" || ids["@+id/widget_scene"] != "ImageView" {
			t.Fatal("missing intrinsic bitmap canvas")
		}
		for _, name := range []string{"start", "unlock", "lock", "weather", "background_previous", "background_next", "style_previous", "style_next", "status", "map", "recenter", "settings"} {
			if ids["@+id/widget_"+name] != "ImageView" || ids["@+id/widget_"+name+"_frame"] != "FrameLayout" {
				t.Fatalf("missing bitmap or exact-padding hit wrapper: %s", name)
			}
		}
	}
}

func TestNativeBitmapCanvasOwnsItsIntrinsicSize(t *testing.T) {
	for _, kind := range []string{"compact", "full"} {
		data, err := os.ReadFile("res/layout/suburban_widget_" + kind + ".xml")
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(strings.NewReader(string(data)))
		found := map[string]bool{}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			attrs := map[string]string{}
			for _, a := range start.Attr {
				attrs[a.Name.Local] = a.Value
			}
			id := attrs["id"]
			if id == "@+id/widget_canvas" || id == "@+id/widget_scene" {
				found[id] = true
				if attrs["layout_width"] != "wrap_content" || attrs["layout_height"] != "wrap_content" {
					t.Fatal("shared bitmap still stretches to guessed host dimensions")
				}
				if id == "@+id/widget_canvas" && attrs["layout_gravity"] != "center" {
					t.Fatal("intrinsic canvas not centered")
				}
			}
			if strings.HasPrefix(id, "@+id/widget_scene_") {
				t.Fatal("obsolete segment path retained")
			}
		}
		if len(found) != 2 {
			t.Fatal("intrinsic canvas/bitmap missing")
		}
	}
}

func TestOnlyExplicitWidgetControlsRouteCommands(t *testing.T) {
	for _, name := range []string{"START", "UNLOCK", "LOCK"} {
		action := actionPrefix + name
		command, ok := commandFor(action)
		if !ok || command == "" || !acceptedAction(action) {
			t.Fatalf("control %s was not routed", name)
		}
	}
	for _, action := range []string{actionPrefix + "BACKGROUND_PREVIOUS", actionPrefix + "BACKGROUND_NEXT", actionPrefix + "STYLE_PREVIOUS", actionPrefix + "STYLE_NEXT", actionPrefix + "REFRESH", actionPrefix + "SYNC", "android.appwidget.action.APPWIDGET_UPDATE", "android.appwidget.action.APPWIDGET_OPTIONS_CHANGED", "android.intent.action.DATE_CHANGED", "android.intent.action.TIMEZONE_CHANGED", "android.intent.action.TIME_SET"} {
		if _, ok := commandFor(action); ok || !acceptedAction(action) {
			t.Fatalf("display event routed command: %s", action)
		}
		if action != actionPrefix+"SYNC" && action != "android.appwidget.action.APPWIDGET_UPDATE" && needsNetwork(action) {
			t.Fatalf("presentation event used network: %s", action)
		}
	}
	for _, action := range []string{"", "START", actionPrefix + "start", actionPrefix + "EV_CHARGE", actionPrefix + "START/UNLOCK", actionPrefix + "LOCK?key=synthetic"} {
		if _, ok := commandFor(action); ok || acceptedAction(action) {
			t.Fatalf("unknown event accepted: %s", action)
		}
	}
}

func TestCommandLabelsDoNotClaimVehicleState(t *testing.T) {
	for state, want := range map[string]string{"queued": "Command queued", "submitted": "Command sent", "succeeded": "Provider reported success", "failed": "Command failed", "accepted": "Command outcome unknown · check in app", "outcome-unknown": "Command outcome unknown · check in app"} {
		if got := commandLabel(state); got != want {
			t.Fatalf("%s: %q", state, got)
		}
	}
}
