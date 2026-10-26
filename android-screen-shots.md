# Android screenshots and design references

Design previews and installed-app captures are separate evidence. The 1.0.8
release remains held until both native widgets and the release checks in
[plan.md](plan.md) pass. Images below do not prove those pending checks.

## Generated design previews — not installed app captures

The approved home design uses a hunter-green background, three action icons,
vehicle information, a map and a Last Updated footer. Values and map geometry
in this design are fictional. Action icons do not indicate physical vehicle state.

![Generated home design with fictional map](artifacts/single-screen-mockup/s9-hunter-green-v5.png)

The configuration references show style selection, linked colors, preview and
Save/Cancel. The implementation also requires Automatic holiday selection with
manual override; the older preview's “Off” label is not that final behavior.

![Generated normal appearance design](artifacts/theme-config-review/configuration/appearance-normal.png)

![Generated birthday appearance design](artifacts/theme-config-review/configuration/appearance-birthday-jan20.png)

## Rendered implementation previews — not device captures

These images come from the implemented Go UI in headless Preview mode. Values
and map lines are fictional. They verify layout and assets, not an installed
Android build, owner login, real telemetry or widget behavior.

![Implemented home preview](artifacts/appearance-implementation/preview-day-portrait.png)

![Implemented appearance gallery](artifacts/appearance-implementation/appearance-portrait.png)

[Landscape home](artifacts/appearance-implementation/preview-day-landscape.png)
and [compact landscape settings](artifacts/appearance-implementation/appearance-compact-landscape.png)
have separate bounds checks. The
[vehicle asset receipt](artifacts/appearance-implementation/vehicle-asset.json)
records the approved black-trim vehicle extraction.

## Shared app and native widget fixtures — not device captures

The shared scene produces the app raster and the native widget bitmap segments.
The fixture test reassembles those segments and compares every pixel and named
hit rectangle with the app adapter at three widths and two font scales. Compact
removes only the map and footer. These fixtures do not prove Android launcher
placement, session continuity, live map delivery, or device rendering.

![Fixture app, full and compact comparison](artifacts/widget-visual-parity/enabled-app-full-compact.png)

[Signed out](artifacts/widget-visual-parity/signed-out-app-full-compact.png),
[pending](artifacts/widget-visual-parity/pending-app-full-compact.png),
[unknown outcome](artifacts/widget-visual-parity/unknown-app-full-compact.png),
[stale](artifacts/widget-visual-parity/stale-app-full-compact.png),
[Christmas](artifacts/widget-visual-parity/christmas-app-full-compact.png), and
[custom colors](artifacts/widget-visual-parity/custom-colors-app-full-compact.png).

## Actual installed-app captures — historical release 1.0.6

These captures are from the SM-G965U running Android 10. The retained
[device receipt](artifacts/android-release-6/device-proof.json) identifies
versionCode 6, source commit e9118b710c17bcd233bd77d41aabeac8f041e711,
installation and retained owner session. They show the previous interface,
not the new design or native widgets. No new vehicle actuation was performed
for those checks. A service-check time is not a vehicle observation time.

![Installed 1.0.6 home screen](artifacts/android-release-6/home-live.png)

![Installed 1.0.6 settings screen](artifacts/android-release-6/settings-live.png)

## Actual installed 1.0.8 candidate — unfinished, widget visuals rejected

These are actual S9 captures, not generated mockups. The app captures below use
explicit Preview with fictional data. The widgets were signed out and displayed
unavailable data. The owner rejected the separate widget layout; it is not visual
acceptance and must be replaced with the app's shared visual definition.

![Installed app in explicit Preview](artifacts/android-runtime-8/home-preview-portrait.png)

[Landscape Preview](artifacts/android-runtime-8/home-preview-landscape.png),
[Automatic appearance selection](artifacts/android-runtime-8/appearance-automatic.png),
and [signed-out account](artifacts/android-runtime-8/account-signed-out.png).

![Rejected compact widget layout](artifacts/android-runtime-8/compact-rejected.png)

![Rejected full widget layout](artifacts/android-runtime-8/full-rejected.png)

The [runtime receipt](artifacts/android-runtime-8/verification.json) records the
limits. No vehicle command was tapped. The candidate was installed before all
requirements were complete. Further installations and publication are held.

## Remaining release evidence

Actual captures of the corrected shared app/widget visuals, saved appearance,
authenticated real map and complete runtime checks are still required.
Portrait and landscape bounds, restart, offline behavior and command mocks
must be checked on the installed build. Generated previews cannot satisfy
these device checks.
