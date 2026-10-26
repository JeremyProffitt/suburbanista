package nativewidgets

const actionPrefix = "ninja.jeremy.suburbanremote.WIDGET_"

// Android's coarse single-update delivery is acknowledged without reading its
// location extras, opening owner storage, rendering, or invoking a command.
const locationFixReadyAction = "ninja.jeremy.suburbanremote.LOCATION_FIX_READY"

func locationFixAcknowledgment(action string) bool { return action == locationFixReadyAction }

func needsNetwork(action string) bool {
	if _, control := commandFor(action); control {
		return true
	}
	return action == "android.appwidget.action.APPWIDGET_UPDATE" || action == actionPrefix+"SYNC"
}

func commandFor(action string) (string, bool) {
	switch action {
	case actionPrefix + "START":
		return "start", true
	case actionPrefix + "UNLOCK":
		return "unlock", true
	case actionPrefix + "LOCK":
		return "lock", true
	}
	return "", false
}
func acceptedAction(action string) bool {
	if _, control := commandFor(action); control {
		return true
	}
	if _, _, ok := appearanceNavigation(action); ok {
		return true
	}
	switch action {
	case "android.appwidget.action.APPWIDGET_UPDATE", "android.appwidget.action.APPWIDGET_OPTIONS_CHANGED", "android.intent.action.TIMEZONE_CHANGED", "android.intent.action.DATE_CHANGED", "android.intent.action.TIME_SET", actionPrefix + "REFRESH", actionPrefix + "SYNC":
		return true
	}
	return false
}

func appearanceNavigation(action string) (buttons bool, direction int, ok bool) {
	switch action {
	case actionPrefix + "BACKGROUND_PREVIOUS":
		return false, -1, true
	case actionPrefix + "BACKGROUND_NEXT":
		return false, 1, true
	case actionPrefix + "STYLE_PREVIOUS":
		return true, -1, true
	case actionPrefix + "STYLE_NEXT":
		return true, 1, true
	}
	return false, 0, false
}

func commandLabel(state string) string {
	switch state {
	case "queued":
		return "Command queued"
	case "dispatching":
		return "Command sending"
	case "submitted":
		return "Command sent"
	case "succeeded":
		return "Provider reported success"
	case "failed", "rejected", "expired":
		return "Command failed"
	default:
		return "Command outcome unknown · check in app"
	}
}
