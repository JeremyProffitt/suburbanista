# Setup

This page sets up everything for the Suburban Remote owner. Work on `main`, push to deploy, never deploy from a laptop. Full delivery rules live in [deploy.md](../deploy.md).

## What you need first

- Windows owner machine with `cmd.exe`, Go toolchain, GitHub CLI, ASK CLI 2.30.7 via the helper, and Playwright Chromium.
- Access to repo `JeremyProffittOrg/Suburban-Remote` and AWS account `759775734231`.
- Your Amazon owner account, your VIN, and your myChevrolet access for the GM sign-in.

## Attended secrets pass

Run each entry from your own `cmd.exe` window. Open printed Amazon links yourself. Type secrets only in hidden local prompts. Helpers check the exact repo and keep existing values.

```bat
"C:\dev\Suburban-Remote\scripts\secretctl.bat" generate
"C:\dev\Suburban-Remote\scripts\secretctl.bat" amazon
"C:\dev\Suburban-Remote\scripts\secretctl.bat" lwa
"C:\dev\Suburban-Remote\scripts\onstar-enroll.bat"
```

What each step stores:

- `generate` creates missing encryption, manifest-signing, device-identity, and Android-signing secrets. Recovery defaults to `%USERPROFILE%\.suburban-remote\recovery` with Windows DPAPI.
- `amazon` uses ASK CLI to get the attended management grant. Stores `ASK_REFRESH_TOKEN` and `ASK_VENDOR_ID`.
- `lwa` guides one shared Login with Amazon profile for web, mobile, and Alexa, then stores `LWA_CLIENT_SECRET`, `LWA_CLIENT_ID`, and `OWNER_AMAZON_USER_ID`. Register the web callback on the profile, then add console-supplied Alexa return URLs. See [lwa.md](lwa.md).
- `onstar-enroll.bat` takes the VIN in memory, opens fresh Playwright Chromium, you sign in with MFA, helper captures the final redirect in memory and pushes a one-use seed to GitHub. Probes are read-only. Two attempts plus a six-hour cooldown bound failures. `--install-browser` installs the pinned browser once on a new machine.

Never paste a credential into chat, logs, or source. To add or rotate a secret later, run `./scripts/set-secret.sh NAME` and type the value in its hidden prompt.

## GitHub variables and what they do

Non-secret config lives in variables. Set with `gh variable set NAME --body "value"`. Current list from `gh variable list`:

- `ADMIN_USER` holds the owner name. Used for owner tagging and status mail.
- `DOMAIN_NAME` holds `suburban.jeremy.ninja`. Feeds HttpApi custom domain, callbacks, and skill endpoint.
- `HOSTED_ZONE_ID` holds the Route53 zone for the domain. Verifies DNS before deploy.
- `CERTIFICATE_ARN` holds the us-east-1 ACM cert for the API domain. Deploy precheck requires ISSUED.
- `CERTIFICATE_ARN_US_EAST_2` holds the second-region cert record. Kept for future use.
- `CLOUDFORMATION_S3_BUCKET` holds the SAM artifact bucket. Package uploads here.
- `DEPLOY_AWS_ACCOUNT` holds `759775734231`. Deploy precheck fails on any other account.
- `AWS_DEPLOY_ROLE_ARN` holds the `gha-deploy` OIDC role. The only AWS auth for CI. No static keys.
- `ALEXA_SKILL_ID` holds `amzn1.ask.skill.3464130d-...`. Pins the development skill identity. See [alexa.md](alexa.md).
- `ASK_VENDOR_ID` holds the ASK developer vendor. Proves the enrollment can see both skills.
- `LWA_CLIENT_ID` holds the shared Login with Amazon client ID. Used by API, worker, app, and Alexa linking. See [lwa.md](lwa.md).
- `OWNER_AMAZON_USER_ID` holds the verified owner `amzn1.account...` ID. Server checks every command against it.
- `ANDROID_KEY_ALIAS` holds the release key alias. Signs candidate APKs in CI.
- `ANDROID_SIGNING_CERT_SHA256` holds the release cert fingerprint. Pinned by the app update check.
- `UPDATE_MANIFEST_PUBLIC_KEY` holds the manifest signature key. Verifies in-app update metadata.
- `CREDENTIAL_ENCRYPTION_KEY_ACTIVE` holds `A` or `B`. Selects which envelope key decrypts DynamoDB state.
- `ENROLLMENT_VERSION` holds the credential generation. Import preserves newer rotations.

Secrets referenced by workflows, never printed:

- `LWA_CLIENT_SECRET`, `CREDENTIAL_ENCRYPTION_KEY_A`, `CREDENTIAL_ENCRYPTION_KEY_B`, `ONSTAR_DEVICE_ID`, `MAP_READ_KEY_SHA256`, `ASK_REFRESH_TOKEN`, `ANDROID_SIGNING_KEYSTORE_B64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_PASSWORD`, `UPDATE_MANIFEST_SIGNING_KEY`, `LOCATION_GEOFENCES_JSON`, one-use `ONSTAR_TOKEN_BOOTSTRAP`.

## Map key

Owner-authorized: generate once on the machine, keep raw value only in SSM `suburban-map-key` in `us-east-1`, standard `SecureString`. APK and GitHub get only the SHA256 digest.

```bat
"C:\dev\Suburban-Remote\scripts\secretctl.bat" map-key --ssm
```

Then `map-key --verify-ssm` checks valid, missing, wrong, and scope-denied keys with cached GET only. Deploy by pushing `main`. Deleting the digest secret plus a deploy disables keyed reads. Owner sessions still work. See [location-map.md](location-map.md).

## Geofences

Home and Work centers live only in SSM `suburbanista-geo` (`us-east-1`, standard `SecureString`). The API, worker, and map resolver read it at cold start. Store the JSON in the `LOCATION_GEOFENCES_JSON` secret, then run the `Refresh private vehicle map` workflow with action `configure-geofences` to write and verify the parameter. Deploy afterwards so every Lambda picks up the new centers. The format is in the [README](../README.md#private-configuration-in-ssm).

## Deploy and verify

Push to `main`. The `Deploy production` workflow tests with race and shuffle, runs DynamoDB Local integration, verifies the template against `infra/deployed-schema.yaml`, builds SAM, imports a one-use seed once, then checks `/healthz` commit match and canary fingerprints.

```bat
set "GOWORK=off"
go test ./... -count=1
go run ./cmd/verify template --schema-baseline infra/deployed-schema.yaml
```

Watch the run with `gh run watch <id>`. A green deploy is not proof of GM access, Alexa linking, or widget acceptance. Those gates live in `plan.md`:

- Read-only OnStar Lambda proof with `live=true` for the exact `main` commit. Budget is three invocations per enrollment.
- App plus full-widget plus compact-widget runtime gates on the S9 before any publication.
- Alexa account-linking consent and command-control attestations before voice claims.

## Screenshots to grab along the way

- Helper success lines with secrets redacted.
- AWS console stack outputs for `MapURL`, `WorkQueueURL`, and `LocationScheduleArn`.
- Alexa developer console skill status and account-linking fields redacted. Store under `artifacts/alexa-linking-*` with dates.
- Login with Amazon console profile callbacks redacted. Link from [lwa.md](lwa.md).
- S9 app and both widgets, labeled Preview or Live, with stale cues kept visible. See [../android-screen-shots.md](../android-screen-shots.md).
