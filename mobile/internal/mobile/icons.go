package mobile

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func vehicleSVG(name, path string) fyne.Resource {
	return theme.NewThemedResource(fyne.NewStaticResource(name, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><g fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">`+path+`</g></svg>`)))
}

var carIcon = vehicleSVG("vehicle.svg", `<path d="M3 15V9l3-4h11l4 4v6H3Z M6 9h12 M6 15v3 M18 15v3 M3 12h3 M18 12h3"/>`)
var lockIcon = vehicleSVG("lock.svg", `<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V6a4 4 0 0 1 8 0v4 M12 14v3"/>`)
var unlockIcon = vehicleSVG("unlock.svg", `<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V6a4 4 0 0 1 8 0 M12 14v3"/>`)
var cargoIcon = vehicleSVG("cargo.svg", `<path d="M4 20V10l3-5h10l3 5v10H4Z M7 5l-2-3h14l-2 3 M4 14h16 M10 17h4"/>`)
var locationIcon = theme.NewThemedResource(fyne.NewStaticResource("location.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#fff" fill-rule="evenodd" d="M12 2a7 7 0 0 0-7 7c0 5 7 13 7 13s7-8 7-13a7 7 0 0 0-7-7zm0 4a3 3 0 1 1 0 6 3 3 0 0 1 0-6z"/></svg>`)))
var fuelIcon = vehicleSVG("fuel.svg", `<path d="M4 21V3h10v18M4 10h10M2 21h14M14 8h3l3 4v6a2 2 0 0 1-4 0v-4M18 5l3 3v4"/>`)
var gaugeIcon = vehicleSVG("gauge.svg", `<path d="M3 18a9 9 0 1 1 18 0M12 13l5-6M6 8l1 1M12 4v2M18 8l-1 1"/>`)
var powerIcon = theme.NewThemedResource(fyne.NewStaticResource("power.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#fff" d="M6.3 4.3a9 9 0 1 0 11.4 0l-1.4 1.4a7 7 0 1 1-8.6 0zM11 1h2v11h-2z"/></svg>`)))

func featureIcon(id string) fyne.Resource {
	switch id {
	case "start":
		return powerIcon
	case "cancel-start", "cancel-alert", "stop-lights":
		return theme.MediaStopIcon()
	case "lock":
		return lockIcon
	case "unlock":
		return unlockIcon
	case "lock-trunk", "unlock-trunk":
		return cargoIcon
	case "horn-and-lights":
		return theme.VolumeUpIcon()
	case "flash-lights":
		return theme.VisibilityIcon()
	case "diagnostics":
		return theme.InfoIcon()
	case "garage", "vehicle-details":
		return carIcon
	case "sxm-subscription":
		return theme.MediaMusicIcon()
	case "onstar-plan":
		return theme.DocumentIcon()
	case "recalls":
		return theme.WarningIcon()
	case "warranty":
		return theme.DocumentIcon()
	case "location":
		return locationIcon
	}
	return theme.InfoIcon()
}
func featureCaption(id string) string {
	switch id {
	case "start":
		return "Start"
	case "cancel-start":
		return "Stop engine"
	case "lock":
		return "Lock"
	case "unlock":
		return "Unlock"
	case "lock-trunk":
		return "Lock cargo"
	case "unlock-trunk":
		return "Unlock cargo"
	case "horn-and-lights":
		return "Horn + lights"
	case "cancel-alert":
		return "Stop alerts"
	case "flash-lights":
		return "Flash lights"
	case "stop-lights":
		return "Stop lights"
	case "diagnostics":
		return "Health"
	case "garage":
		return "Garage"
	case "vehicle-details":
		return "Details"
	case "onstar-plan":
		return "OnStar plan"
	case "recalls":
		return "Recalls"
	case "warranty":
		return "Warranty"
	case "sxm-subscription":
		return "SiriusXM"
	case "location":
		return "Location"
	}
	return "Feature"
}
