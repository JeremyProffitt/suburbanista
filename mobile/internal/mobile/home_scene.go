package mobile

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/driver/mobile"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/appearance"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/homescene"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/mapview"
	"github.com/JeremyProffittOrg/suburban-remote/mobile/internal/widgetdisplay"
)

func (s *Screen) buildSingleHome() fyne.CanvasObject {
	h := &homeView{}
	s.home = h
	text := func() *canvas.Text { return canvas.NewText("", color.White) }
	h.temperature = text()
	h.highLow = text()
	h.gas = text()
	h.rangeText = text()
	h.mileage = text()
	h.mapLabel = text()
	h.mode = text()
	h.lastUpdated = text()
	h.weatherIcon = canvas.NewImageFromResource(theme.QuestionIcon())
	h.car = canvas.NewImageFromResource(cutoutResource)
	h.mapImage = canvas.NewRaster(func(w, h int) image.Image { return s.mapPicture(w, h) })
	h.fuelGauge = canvas.NewRaster(func(w, h int) image.Image { return image.NewNRGBA(image.Rect(0, 0, w, h)) })
	h.sky = canvas.NewRaster(func(w, height int) image.Image {
		return s.renderHomeScene(w, height)
	})
	h.hits = map[string]fyne.CanvasObject{}
	for _, id := range []string{"start", "unlock", "lock"} {
		b := s.quickButtons[id]
		b.hitOnly = true
		b.touchGuarded = true
		h.hits[id] = b
	}
	h.actions = container.NewWithoutLayout(s.start, s.unlock, s.lock)
	h.gear = newGlyph("Settings", theme.SettingsIcon(), s.showSettings)
	h.gear.hitOnly = true
	h.hits["settings"] = h.gear
	for _, entry := range []struct {
		id, label string
		fn        func()
	}{{"background-previous", "Previous background", func() { s.navigateAppearance(false, -1) }}, {"background-next", "Next background", func() { s.navigateAppearance(false, 1) }}, {"style-previous", "Previous button style", func() { s.navigateAppearance(true, -1) }}, {"style-next", "Next button style", func() { s.navigateAppearance(true, 1) }}} {
		b := newGlyph(entry.label, nil, entry.fn)
		b.hitOnly = true
		h.hits[entry.id] = b
	}
	hold := &weatherHold{screen: s}
	hold.ExtendBaseWidget(hold)
	h.hits["weather"] = hold
	h.mapPanel = s.newMapInteraction()
	h.hits["map"] = h.mapPanel
	attribution := newGlyph("Map data attribution", nil, s.openMapAttribution)
	attribution.hitOnly = true
	h.hits["attribution"] = attribution
	status := newGlyph("Check command status", nil, func() { s.run(s.latestActivity) })
	status.hitOnly = true
	h.hits["status"] = status
	recenter := newGlyph("Recenter map", nil, s.recenterMap)
	recenter.hitOnly = true
	h.hits["recenter"] = recenter
	updated := newGlyph("Refresh vehicle location", nil, s.requestLocationRefresh)
	updated.hitOnly = true
	h.hits["location-refresh"] = updated
	h.root = container.New(&sceneLayout{screen: s}, h.sky, h.actions)
	h.root.Add(h.mapPanel)
	for id, o := range h.hits {
		if b, ok := o.(*iconAction); ok {
			b.onTouchStart = s.cancelCommandTouches
		}
		if id != "start" && id != "unlock" && id != "lock" && id != "map" {
			h.root.Add(o)
		}
	}
	freeze := newGlyph("Hold appearance rotation", nil, s.toggleScreenLock)
	freeze.hitOnly = true
	freeze.onTouchStart = s.cancelCommandTouches
	h.hits["screen-lock"] = freeze
	h.root.Add(freeze)
	s.paintHome()
	return h.root
}

type sceneLayout struct{ screen *Screen }

func (*sceneLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(180, 320) }
func (l *sceneLayout) Layout(_ []fyne.CanvasObject, size fyne.Size) {
	h := l.screen.home
	if h == nil || size.Width <= 0 || size.Height <= 0 {
		return
	}
	width := min(size.Width, size.Height*360/640)
	height := size.Height
	x, y := (size.Width-width)/2, (size.Height-height)/2
	h.sky.Move(fyne.NewPos(x, y))
	h.sky.Resize(fyne.NewSize(width, height))
	h.actions.Move(fyne.NewPos(x, y))
	h.actions.Resize(fyne.NewSize(width, height))
	regions := homescene.LayoutRegions(h.input, 360, int(height*360/width+.5), false)
	for name, o := range h.hits {
		r, ok := regions[name]
		if !ok {
			o.Hide()
			continue
		}
		if name != "start" && name != "unlock" && name != "lock" {
			o.Show()
		}
		dx, dy := x, y
		if name == "start" || name == "unlock" || name == "lock" {
			dx, dy = 0, 0
		}
		o.Move(fyne.NewPos(dx+float32(r.Min.X)*width/360, dy+float32(r.Min.Y)*width/360))
		o.Resize(fyne.NewSize(float32(r.Dx())*width/360, float32(r.Dy())*width/360))
	}
}
func AppSceneFrame(in homescene.Input, width int, compact bool) homescene.Frame {
	return homescene.Render(in, width, compact)
}
func AppSceneFrameSize(in homescene.Input, width, height int, compact bool) homescene.Frame {
	return homescene.RenderSize(in, width, height, compact)
}
func (s *Screen) sceneInput(now time.Time) homescene.Input {
	settings, local, _ := s.displayAppearance(now)
	h := s.home
	in := homescene.Input{Settings: settings, Now: local, Allowed: s.allowedActions, FontScale: 1}
	if h == nil {
		return in
	}
	in.FontScale = h.fontScale
	in.ScreenLockVisible = true
	in.ScreenLocked = h.rotationLocked.Load()
	in.MapRevision = h.mapRevision
	in.HighLow = h.highLow.Text
	in.Temperature = h.temperature.Text
	in.Range = h.rangeText.Text
	if s.homeTelemetry.Range.Available {
		in.RangeFreshness = s.homeTelemetry.Range.Freshness
	}
	in.Fuel = h.gas.Text
	if s.homeTelemetry.Fuel.Available {
		percent := s.homeTelemetry.Fuel.Value
		in.FuelPercent = &percent
		in.FuelFreshness = s.homeTelemetry.Fuel.Freshness
		in.Fuel = fmt.Sprintf("%.0f%%", s.homeTelemetry.Fuel.Value)
	}
	in.Odometer = h.mileage.Text
	in.Updated = "Last Updated · " + s.sourceUpdateLabel()
	in.Status = s.commandStatus.Text
	if s.preview.Load() {
		in.RangeFreshness = ""
		in.Allowed = map[string]bool{"start": true, "unlock": true, "lock": true}
		in.Status = "PREVIEW · fictional map and sample readings"
		if strings.HasPrefix(s.commandStatus.Text, "Preview · ") {
			in.Status = s.commandStatus.Text + " · fictional map"
		}
		in.Fuel = "62%"
		percent := 62.0
		in.FuelPercent = &percent
	}
	if s.vehicleMapImage != nil || s.preview.Load() {
		in.MapRenderer = s.mapPicture
		in.Attribution = s.mapView.Attribution
		in.MapNoticeHeight = float64(mapview.ViewNoticeHeight(s.mapView, 360, 246))
	}
	actual, w := s.homeWeather()
	s.environmentMu.Lock()
	weatherReady := s.weatherState == "ready"
	s.environmentMu.Unlock()
	if w != nil && (s.preview.Load() || weatherReady) && actual.Sub(w.FetchedAt) <= 30*time.Minute {
		in.Weather = sceneWeatherCondition(actual, w)
		in.Night = actual.Before(w.Sunrise) || !actual.Before(w.Sunset)
		in.Sunrise = w.Sunrise
		in.Sunset = w.Sunset
	}
	condition, preview := h.preview.Condition(now, in.Weather)
	in.Weather = condition
	if preview {
		in.Status = "WEATHER PREVIEW · tap weather to cancel"
		in.Night = condition == "Night"
		in.Sunrise = time.Time{}
		in.Sunset = time.Time{}
	}
	in.LocationRefreshing, in.RefreshAngle = s.sceneRefreshState()
	return in
}

func (s *Screen) openMapAttribution() {
	s.open("https://docs.aws.amazon.com/location/latest/developerguide/data-attribution.html")
}
func sceneWeatherCondition(now time.Time, w *Weather) string {
	if w == nil {
		return ""
	}
	return widgetdisplay.ConditionAt(w.Code, now, w.Sunrise, w.Sunset)
}

func (s *Screen) navigateAppearance(buttons bool, direction int) {
	var backdrop string
	var style appearance.Style
	changed := s.mutateAppearance(func(settings *appearance.Settings) {
		_, local, _ := s.presentationAt(*settings, time.Now())
		if s.rotationIsLocked() {
			held := s.heldAppearance(*settings)
			if buttons {
				held.NavigateButtons(direction, local)
				style, _ = held.SelectedStyleAt(local)
				if style.Holiday == "" {
					settings.NormalStyle = style.ID
				} else {
					settings.HolidayStyles[style.Holiday] = style.ID
				}
				settings.ButtonRotation.Epoch = local
			} else {
				held.NavigateBackground(direction, local)
				backdrop = held.Backdrop
				settings.Backdrop, settings.BackgroundRotation.Epoch = backdrop, local
			}
			return
		}
		if buttons {
			settings.NavigateButtons(direction, local)
		} else {
			settings.NavigateBackground(direction, local)
		}
	})
	if changed && s.rotationIsLocked() {
		if buttons {
			s.home.lockedStyle = style
		} else {
			s.home.lockedBackdrop = backdrop
		}
		s.paintHome()
	}
}
func (s *Screen) appearanceOpened() {
	if s.appearanceForeground {
		return
	}
	s.appearanceForeground = true
	if s.home != nil {
		s.home.metadataAt = time.Time{}
	}
	if platformWidgetsAvailable() {
		s.appearance = appearance.Settings{}
	}
	if s.appearanceInternalReturn {
		s.appearanceInternalReturn = false
		return
	}
	if s.rotationIsLocked() {
		return
	}
	now := time.Now()
	s.appearanceOpening = strconv.FormatInt(now.UnixNano(), 10)
	s.mutateAppearance(func(settings *appearance.Settings) {
		_, local, _ := s.presentationAt(*settings, now)
		settings.Open(s.appearanceOpening, local)
	})
}
func (s *Screen) appearanceClosed() {
	s.cancelCommandTouches()
	s.appearanceForeground = false
	if s.home != nil {
		s.home.preview.Cancel()
		s.home.previewGeneration++
	}
}

type weatherHold struct {
	widget.BaseWidget
	screen *Screen
}

func (w *weatherHold) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.NRGBA{}))
}
func (w *weatherHold) MinSize() fyne.Size { return fyne.NewSize(48, 48) }
func (w *weatherHold) AccessibilityLabel() string {
	return "Weather. Hold for fifteen seconds to preview weather effects. Tap again to cancel."
}
func (w *weatherHold) press() {
	s := w.screen
	s.cancelCommandTouches()
	now := time.Now()
	if s.home.preview.Active() {
		s.home.preview.Cancel()
		s.home.previewGeneration++
		s.refreshSceneInput(now)
		s.home.sky.Refresh()
		return
	}
	_, weather := s.homeWeather()
	s.home.preview.Press(now, sceneWeatherCondition(now, weather))
	s.home.previewGeneration++
	generation := s.home.previewGeneration
	go func() {
		timer := time.NewTicker(100 * time.Millisecond)
		defer timer.Stop()
		for i := 0; i < 710; i++ {
			<-timer.C
			done := make(chan bool, 1)
			fyne.Do(func() {
				if s.home == nil || s.home.previewGeneration != generation {
					done <- true
					return
				}
				s.refreshSceneInput(time.Now())
				s.home.sky.Refresh()
				done <- false
			})
			if <-done {
				return
			}
		}
	}()
}
func (w *weatherHold) release() {
	h := w.screen.home
	h.preview.Condition(time.Now(), "")
	h.preview.Release()
	if !h.preview.Active() {
		h.previewGeneration++
	}
}
func (w *weatherHold) Tapped(*fyne.PointEvent)        {}
func (w *weatherHold) MouseDown(*desktop.MouseEvent)  { w.press() }
func (w *weatherHold) MouseUp(*desktop.MouseEvent)    { w.release() }
func (w *weatherHold) MouseIn(*desktop.MouseEvent)    {}
func (w *weatherHold) MouseMoved(*desktop.MouseEvent) {}
func (w *weatherHold) MouseOut()                      { w.abort() }
func (w *weatherHold) TouchDown(*mobile.TouchEvent)   { w.press() }
func (w *weatherHold) TouchUp(*mobile.TouchEvent)     { w.release() }
func (w *weatherHold) TouchCancel(*mobile.TouchEvent) { w.abort() }

func toolbarRange(value float64, unit string) string {
	if strings.EqualFold(unit, "mi") {
		unit = "Miles"
	}
	return fmt.Sprintf("%.0f %s", value, unit)
}

func (s *Screen) sourceUpdateLabel() string {
	now := time.Now()
	if !s.preview.Load() && mapview.Valid(s.mapView, s.vehicleMapImage, now) {
		place := ""
		if s.mapView.Place == "Home" || s.mapView.Place == "Work" {
			place = s.mapView.Place + " · "
		}
		if s.mapView.ObservedAt.IsZero() {
			return place + "source time unknown"
		}
		if s.mapView.ObservedAt.After(now.Add(time.Minute)) {
			return place + "Time unknown"
		}
		return place + s.displayTime(s.mapView.ObservedAt)
	}
	var observed time.Time
	for _, r := range []TelemetryReading{s.homeTelemetry.Fuel, s.homeTelemetry.Range, s.homeTelemetry.Odometer} {
		if r.Available && r.ObservedAt != nil && r.ObservedAt.After(observed) && !r.ObservedAt.After(now.Add(time.Minute)) {
			observed = *r.ObservedAt
		}
	}
	if observed.IsZero() {
		return "source time unknown"
	}
	return s.displayTime(observed)
}

func (w *weatherHold) abort() {
	h := w.screen.home
	h.preview.Cancel()
	h.previewGeneration++
	w.screen.refreshSceneInput(time.Now())
	h.sky.Refresh()
}
