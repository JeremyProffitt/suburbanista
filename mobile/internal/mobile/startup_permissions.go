package mobile

import (
	"sync"

	"fyne.io/fyne/v2"
)

const coarsePermission = "android.permission.ACCESS_COARSE_LOCATION"
const finePermission = "android.permission.ACCESS_FINE_LOCATION"
const notificationPermission = "android.permission.POST_NOTIFICATIONS"

// permissionCoordinator tracks this process only. A prior request is evidence
// for permanent denial only when Android also reports no rationale. It never
// substitutes for reading the current OS grant.
type permissionCoordinator struct {
	sync.Mutex
	attempted map[string]bool
}

type permissionGrant struct {
	Name                                  string
	Applicable, Granted, Rationale, Known bool
}

func (p *permissionCoordinator) requests(grants []permissionGrant, previouslyAsked func(string) bool, explicit bool) (request []string, recovery bool) {
	p.Lock()
	defer p.Unlock()
	if p.attempted == nil {
		p.attempted = map[string]bool{}
	}
	for _, grant := range grants {
		if !grant.Known || !grant.Applicable || grant.Granted {
			continue
		}
		if previouslyAsked(grant.Name) && !grant.Rationale {
			recovery = true
			continue
		}
		if p.attempted[grant.Name] && !explicit {
			continue
		}
		request = append(request, grant.Name)
		p.attempted[grant.Name] = true
	}
	return
}

func (s *Screen) startupPermissionCheck(explicit bool) {
	if s.app == nil || s.window == nil || s.preview.Load() {
		return
	}
	// Explicit Weather refresh runs on the request worker; startup runs on the
	// UI thread. Serialize both through Fyne before changing foreground state.
	fyne.Do(func() { s.startupPermissionCheckOnUI(explicit) })
}

func (s *Screen) startupPermissionCheckOnUI(explicit bool) {
	if s.preview.Load() {
		return
	}
	grants := platformPermissionInventory(s.window)
	request, recovery := s.startupPermissions.requests(grants, func(name string) bool {
		return s.app.Preferences().Bool("permission-requested:" + name)
	}, explicit)
	// Android 12+ requires fine and coarse to be requested together.
	for _, name := range request {
		if name == finePermission {
			found := false
			for _, other := range request {
				found = found || other == coarsePermission
			}
			if !found {
				request = append(request, coarsePermission)
			}
			break
		}
	}
	if len(request) != 0 {
		s.appearanceInternalReturn = true
		if platformRequestPermissions(s.window, request) {
			for _, name := range request {
				s.app.Preferences().SetBool("permission-requested:"+name, true)
			}
		} else {
			s.appearanceInternalReturn = false
		}
	}
	if recovery && s.status != nil {
		s.status.SetText("Permission denied. Open Android Settings, Apps, Suburbanista, Permissions to allow it.")
	}
	s.startBackgroundWork()
}
