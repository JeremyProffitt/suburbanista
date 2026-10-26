# S24 Ultra-sized app screens with satellite maps

Twenty fresh current-app renders: 16 portrait screens at 1440 × 3120 and four explicit landscape screens at 3120 × 1440. Portrait ratio is 6:13, equivalent to 9:19.5. Samsung lists this display resolution in its [official S24 series specification](https://www.samsung.com/ae/support/mobile-devices/what-are-the-sizes-and-the-resolution-of-the-new-s24-series/).

These are rendered app previews at S24 Ultra proportions, not captures of a physical S24 Ultra. No older S9 screenshot was stretched. The 12 home scenes are rendered directly at full target resolution by the current `homescene` renderer. Display and option panels use the current Fyne UI at the matching logical ratio, rasterized and uniformly scaled to target pixels.

The map is real AWS satellite imagery of the public Washington Monument / National Mall area. It was fetched once with the app's existing Amazon Location renderer. The app's `mapview` renderer displays the image and demonstration vehicle marker. Provider AWS/HERE attribution remains visible. No owner Home, Work, or current location is used.

Vehicle readings, weather, clock times, and the map marker are demonstration values. Screen labels identify the public demonstration map. There were no vehicle commands, production data writes, cache changes, deployments, or application behavior changes.

The set covers Charlotte, Beach, Yosemite, Grand Canyon, Neon Harbor, Country, Mountains, and Aurora; clear day, dawn, dusk, night, rain, snow, fog, and thunderstorm; eight button styles; appearance, Settings, and rotation options; portrait and landscape display.

Verification: `go test ./internal/locationmap -run '^TestPromoPublicMapTemporary$' -count=1 -timeout=75s` passed. `go test -tags ci ./internal/mobile -run '^TestPromoS24CaptureTemporary$' -count=1 -timeout=180s` passed in 27.615 seconds. All 20 screen PNGs decoded and matched the target portrait or landscape dimensions. Home, settings, landscape, and contact-sheet views were inspected. Temporary source tests were removed; harness text is retained here and in the sibling map folder.

`manifest.json` includes image hashes, dimensions, and source details. `contact-sheet.png` shows the 12 home variations. The shared provider source is `../map/public-demo-satellite.png`.
