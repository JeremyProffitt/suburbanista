//go:build android && widgetbind

package nativewidgets

import (
	"errors"
	"image"
	"math"
	"strings"
	"time"

	"git.wow.st/gmp/jni"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearancecore"
)

func render(env jni.Env, ctx jni.Object, state viewState) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("Widget rendering unavailable")
		}
	}()
	n := newNative(env)
	defer n.close()
	now := time.Now()
	zone, valid := appearancecore.DeviceTimeZone(env)
	local := now
	if valid {
		local = now.In(zone)
	}
	if !valid {
		local = now
	}
	if !valid && state.settings.Mode == appearancecore.Automatic {
		state.settings.Mode = appearancecore.Normal
		state.status = "Local date unavailable · normal style"
	}
	pkg := jni.GoString(env, jni.String(n.call(ctx, "getPackageName", "()Ljava/lang/String;")))
	resources := n.call(ctx, "getResources", "()Landroid/content/res/Resources;")
	id := func(name, kind string) jni.Value {
		value := n.intCall(resources, "getIdentifier", "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)I", n.str(name), n.str(kind), n.str(pkg))
		if value == 0 {
			panic("Missing widget resource")
		}
		return jni.Value(value)
	}
	metrics := n.call(resources, "getDisplayMetrics", "()Landroid/util/DisplayMetrics;")
	metricsClass := n.class("android/util/DisplayMetrics")
	density := float64(jni.GetFloatField(env, metrics, jni.GetFieldID(env, metricsClass, "density", "F")))
	configuration := n.call(resources, "getConfiguration", "()Landroid/content/res/Configuration;")
	configurationClass := n.class("android/content/res/Configuration")
	fontScale := float64(jni.GetFloatField(env, configuration, jni.GetFieldID(env, configurationClass, "fontScale", "F")))
	orientation := jni.GetIntField(env, configuration, jni.GetFieldID(env, configurationClass, "orientation", "I"))
	if density <= 0 || density > 8 {
		return errors.New("Widget density unavailable")
	}
	manager := n.static("android/appwidget/AppWidgetManager", "getInstance", "(Landroid/content/Context;)Landroid/appwidget/AppWidgetManager;", jni.Value(ctx))
	for _, kind := range []string{"compact", "full"} {
		provider := "nativewidgets.CompactProvider"
		if kind == "full" {
			provider = "nativewidgets.FullProvider"
		}
		component := n.new("android/content/ComponentName", []jni.Object{jni.Object(n.class("java/lang/String")), jni.Object(n.class("java/lang/String"))}, []jni.Object{jni.Object(n.str(pkg)), jni.Object(n.str(provider))})
		instances := n.call(manager, "getAppWidgetIds", "(Landroid/content/ComponentName;)[I", jni.Value(component))
		count := n.staticInt("java/lang/reflect/Array", "getLength", "(Ljava/lang/Object;)I", jni.Value(instances))
		if count > 64 {
			return errors.New("Widget instance limit exceeded")
		}
		for index := 0; index < count; index++ {
			widgetID := n.staticInt("java/lang/reflect/Array", "getInt", "(Ljava/lang/Object;I)I", jni.Value(instances), jni.Value(index))
			options := n.call(manager, "getAppWidgetOptions", "(I)Landroid/os/Bundle;", jni.Value(widgetID))
			widthName, heightName := "appWidgetMinWidth", "appWidgetMaxHeight"
			if orientation == 2 {
				widthName, heightName = "appWidgetMaxWidth", "appWidgetMinHeight"
			}
			widthDP := n.intCall(options, "getInt", "(Ljava/lang/String;I)I", n.str(widthName), 250)
			defaultHeight := 280
			if kind == "full" {
				defaultHeight = 440
			}
			heightDP := n.intCall(options, "getInt", "(Ljava/lang/String;I)I", n.str(heightName), jni.Value(defaultHeight))
			width, height := int(math.Round(float64(widthDP)*density)), int(math.Round(float64(heightDP)*density))
			if width <= 0 || height <= 0 || width > 4096 || height > 4096 {
				return errors.New("Widget size unavailable")
			}
			referenceHeight := 640.0
			if kind == "compact" {
				referenceHeight = 349
			}
			renderWidth := min(width, int(float64(height)*360/referenceHeight), 1080)
			renderHeight := int(float64(height) * float64(renderWidth) / float64(width))
			if float64(height)/float64(width) < referenceHeight/360 {
				renderHeight = int(math.Round(float64(renderWidth) * referenceHeight / 360))
			}
			input := sceneInput(state, local, fontScale)
			if !valid {
				input.Updated = "Last Updated · Time unavailable"
			}
			scene, sceneErr := renderSceneSize(input, renderWidth, renderHeight, kind == "compact")
			if sceneErr != nil {
				return errors.New("Widget image unavailable")
			}
			views := n.new("android/widget/RemoteViews", []jni.Object{jni.Object(n.class("java/lang/String")), n.intType()}, []jni.Object{jni.Object(n.str(pkg)), n.integer(int(id("suburban_widget_"+kind, "layout")))})
			padding := func(name string, r image.Rectangle) {
				n.void(views, "setViewPadding", "(IIIII)V", id(name, "id"), jni.Value(r.Min.X), jni.Value(r.Min.Y), jni.Value(scene.Bounds.Dx()-r.Max.X), jni.Value(scene.Bounds.Dy()-r.Max.Y))
			}
			data := jni.NewByteArray(env, scene.PNG)
			n.keep(jni.Object(data))
			bitmap := n.static("android/graphics/BitmapFactory", "decodeByteArray", "([BII)Landroid/graphics/Bitmap;", jni.Value(data), 0, jni.Value(len(scene.PNG)))
			if bitmap == 0 {
				return errors.New("Widget image unavailable")
			}
			n.void(bitmap, "setDensity", "(I)V", 0)
			n.void(views, "setImageViewBitmap", "(ILandroid/graphics/Bitmap;)V", id("widget_scene", "id"), jni.Value(bitmap))
			packages := n.call(ctx, "getPackageManager", "()Landroid/content/pm/PackageManager;")
			launch := n.call(packages, "getLaunchIntentForPackage", "(Ljava/lang/String;)Landroid/content/Intent;", n.str(pkg))
			if launch == 0 || n.call(launch, "getComponent", "()Landroid/content/ComponentName;") == 0 {
				return errors.New("Suburbanista launcher unavailable")
			}
			open := n.static("android/app/PendingIntent", "getActivity", "(Landroid/content/Context;ILandroid/content/Intent;I)Landroid/app/PendingIntent;", jni.Value(ctx), 2000, jni.Value(launch), 0x0c000000)
			for _, name := range []string{"start", "unlock", "lock", "weather", "background-previous", "background-next", "style-previous", "style-next", "status", "map", "recenter", "settings", "attribution"} {
				resource := "widget_" + strings.ReplaceAll(name, "-", "_")
				r, exists := scene.Regions[name]
				if !exists {
					n.void(views, "setViewVisibility", "(II)V", id(resource+"_frame", "id"), 8)
					continue
				}
				padding(resource+"_frame", r)
				pending := open
				action := ""
				enabled := true
				switch name {
				case "start", "unlock", "lock":
					action = strings.ToUpper(name)
					enabled = state.allowed[name]
				case "status":
					action = "SYNC"
				case "background-previous", "background-next", "style-previous", "style-next":
					action = strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
				}
				if action != "" {
					intent := n.new("android/content/Intent", nil, nil)
					n.call(intent, "setComponent", "(Landroid/content/ComponentName;)Landroid/content/Intent;", jni.Value(component))
					n.call(intent, "setAction", "(Ljava/lang/String;)Landroid/content/Intent;", n.str(actionPrefix+action))
					// Immutable explicit intents contain no credential or provider options.
					pending = n.static("android/app/PendingIntent", "getBroadcast", "(Landroid/content/Context;ILandroid/content/Intent;I)Landroid/app/PendingIntent;", jni.Value(ctx), jni.Value(widgetID), jni.Value(intent), 0x0c000000)
				}
				n.void(views, "setOnClickPendingIntent", "(ILandroid/app/PendingIntent;)V", id(resource, "id"), jni.Value(pending))
				value := jni.Value(0)
				if enabled {
					value = 1
				}
				n.void(views, "setBoolean", "(ILjava/lang/String;Z)V", id(resource, "id"), n.str("setEnabled"), value)
			}
			n.void(manager, "updateAppWidget", "(ILandroid/widget/RemoteViews;)V", jni.Value(widgetID), jni.Value(views))
		}
	}
	return nil
}
