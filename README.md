# Suburbanista

A personal Android dashboard and remote control for a 2026 Chevrolet Suburban. Access is limited to the configured owner through Amazon sign-in.

https://github.com/user-attachments/assets/fd80e127-7dfb-407a-838e-7150438865f8

[Screenshots](artifacts/project-promo/screenshots-s24-ultra/rendered) · [Download video](artifacts/project-promo/suburbanista-promo-1080p.mp4)

## Features

- **Vehicle controls:** request remote start, lock, and unlock, then check command status.
- **Vehicle information:** fuel, estimated range, odometer, and diagnostic readings with source timestamps.
- **Satellite maps:** pan, zoom, and recenter. Opening the app or tapping Center refreshes the map; Last Updated requests a new vehicle location.
- **Location tracking:** background phone updates can supplement vehicle location while the phone and vehicle are together. Home and Work geofences snap the map to private centers stored in SSM.
- **Personalization:** scenic backgrounds, button styles, colors, holiday themes, rotation choices, and weather and daylight effects.
- **Reading history:** hourly fuel, odometer, and other returned diagnostics are stored for future MPG analysis.

## Alexa

Use the linked-owner development skill to request status and vehicle commands:

- “Alexa, ask Suburban for status.”
- “Alexa, tell Suburban to lock.”
- “Alexa, ask Suburban for command status.”

Status includes fuel, range, mileage, and reading age. The skill is US-only and is not published in the Alexa Skills Store. See the [Alexa guide](alexa-readme.md) for supported phrases and limits.

## Access and setup

Install the owner APK from the [private releases page](https://github.com/JeremyProffittOrg/Suburban-Remote/releases), then select **Sign in with Amazon**. Vehicle controls depend on the connected account, vehicle capabilities, and enabled permissions.

An authenticated [MCP integration](docs/owner-tools.md) provides vehicle reads and command requests for compatible assistants.

## Private configuration in SSM

Personal data stays out of the source code. It lives in AWS SSM Parameter Store (`us-east-1`, standard tier, `SecureString` with the AWS-managed `aws/ssm` key):

- `suburbanista-geo`: Home and Work geofence centers as JSON. The API, worker, and map resolver Lambdas read it once at cold start and fail to start if it is missing or invalid.
- `suburban-map-key`: the raw key for keyed map embeds. Only its SHA256 digest reaches GitHub and the APK. See [Map access](docs/location-map.md).

`suburbanista-geo` format. Both entries are required. Radii are fixed at 100 ft (Home) and 200 ft (Work):

```json
{"fences":[
  {"name":"Home","latitude":0.0,"longitude":0.0,"radiusMeters":30.48},
  {"name":"Work","latitude":0.0,"longitude":0.0,"radiusMeters":60.96}
]}
```

To change it, update GitHub secret `LOCATION_GEOFENCES_JSON` with `./scripts/set-secret.sh LOCATION_GEOFENCES_JSON`, then run the `Refresh private vehicle map` workflow with action `configure-geofences`. The workflow validates the JSON, writes `suburbanista-geo`, and reads it back. Running Lambdas keep the old centers until their next cold start, so push to `main` (deploy) to apply the change at once.

Credentials (LWA, encryption keys, signing keys, OnStar device identity) are GitHub secrets passed to Lambda environment variables at deploy time. None are in source. See [Setup](docs/setup.md).

This public copy omits the private run receipts under `artifacts/`, so some receipt links in the guides do not resolve.

[Setup and deployment](deploy.md) · [Map access](docs/location-map.md)

Guides: [Setup](docs/setup.md) | [Alexa](docs/alexa.md) | [Login with Amazon](docs/lwa.md) | [Cost](docs/cost.md)
