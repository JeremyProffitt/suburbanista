# Private vehicle map

Current implementation (2026-10-25): private AWS Satellite imagery is generated
only on a map request. The larger phone canvas is 3072x2048, four times the
previous width and height, with the same initial geographic view. Delivery and
device proof are recorded separately in `plan.md`; compilation alone is not
proof of live image delivery. The historical Overpass failure is retained in
[the earlier receipt](../artifacts/map-access/verification.json).

The owner-approved contract of 2026-10-24 is one cached PNG with two retrieval
paths: an existing LWA-linked owner session, or a dedicated map-only key. The
endpoint is `https://suburban.jeremy.ninja/api/map.png`. The Android app supplies
its normal bearer handle and never stores the map key. No image request calls
the vehicle. S3 blocks all public access; the API role reads immutable images
under `map-cache/`. Current delivery checks the encrypted image reference and
fixed expiry before reading storage.

For a private embed, append `?key=<owner-held-key>` locally. This is a bearer
credential: anyone holding that URL can read the image. Never paste an actual
key or keyed URL into chat, logs, source, screenshots, email or a public page.
The key cannot read history, wake the car, submit commands or create sessions.
Responses use `private, no-store` and `no-referrer`. Authorized GET requests to
`/api/map.png` return 200 with a direct PNG, at most768x512 and3MiB; they do not
redirect. The complete cached canvas, including provider credits, is uniformly
resized rather than cropped. This preserves the living-room display's existing
no-redirect, bounded-image contract and its normalized pin positions. HEAD checks
availability and returns 200 without a body or redirect. There is no anonymous
fallback. The owner-only phone `/api/map/view` route defaults to the original
inline PNG response, using the same bounded full-canvas thumbnail. Updated apps
explicitly request `Accept: application/vnd.suburban.map-ticket+json` to receive
a120-second private S3 ticket for the full3072x2048 image. Treat that URL as a
credential; generic or absent Accept headers never opt older apps into tickets.

Phone map access uses the owner session and has no source-IP restriction. A
valid session works across Wi-Fi and mobile networks. The living-room panel
home-IP/display-key gate belongs to that separate application and does not
restrict this phone endpoint. Missing or invalid owner authorization is still denied.

Owner-approved 2026-10-25 for the living-room display: `GET /api/map/place`
accepts the same key or owner session and returns the current image's `imageId`,
`observedAt`, the geofence `place` (`Home`, `Work` or empty) and the vehicle pin
as `pinX`/`pinY` fractions of the image. It returns no coordinates, reads no
image and never wakes the vehicle.

## Key setup, rotation and revocation

Owner-authorized 2026-10-24: generate the key on the machine and retain the raw
value only in AWS SSM parameter `suburban-map-key`, in account `759775734231`,
region `us-east-1`. The standard-tier parameter is a `SecureString` encrypted
with the AWS-managed SSM key. The APK and GitHub do not receive the raw value.

The idempotent setup command is:

```bat
"C:\dev\Suburban-Remote\scripts\secretctl.bat" map-key --ssm
```

The helper verifies the AWS account and exact-name parameter metadata before
generating 32 cryptographically random bytes in memory. Creation uses
`Overwrite=false`; an existing standard `SecureString` is preserved and reused.
It verifies encrypted-store readback in memory, then sends only the SHA256
digest to repository secret `MAP_READ_KEY_SHA256` on exactly
`JeremyProffittOrg/Suburban-Remote` through GitHub CLI standard input. It never
prints the value or digest, puts them in arguments, or writes them to local files.
Its receipt contains names, type, tier, version, and configuration status only.

After the GitHub deployment, `map-key --verify-ssm` reads the key only in memory
and reports HTTP status codes for the valid key, missing key, wrong key and
command-status scope denial, plus measured image dimensions and byte count.
It polls pending generation and verifies the direct bounded PNG. It also accepts
one validated private S3 redirect for compatibility with the preceding deployment.
A map-cache miss may render imagery, but never wakes the vehicle or sends a
vehicle command. A valid-key `503` proves the authentication path
was reached but does not prove a PNG is available. A raw key or keyed URL must
never be copied into chat, logs, email or command arguments.

Deploy by pushing main after setup or an explicitly authorized rotation. The
setup command never rotates an existing SSM value. Replacing the digest invalidates
the old URL after deployment. Delete the GitHub repository secret and deploy to
disable keyed retrieval; valid owner sessions still work. A missing digest keeps
keyed access disabled; a placeholder key is never installed. No dashboard
adapter or raw-key CI secret is required.

## Scheduled polling and rendering

The existing EventBridge schedule sends a location message to the FIFO queue at
minute 0 and 30 UTC. The worker shares the credential lease with commands, checks
provider pause/cooldown, and persists refreshed credentials before use. Each slot
is claimed before one GPS wake. At most two passive status reads follow. Duplicate
delivery cannot repeat the wake. Rate limits pause for at least 30 minutes;
authentication denial preserves the six-hour cooldown.

Fix history, latest position and slot records use encrypted DynamoDB documents
with 90-day expiry. No Scan, raw history endpoint or coordinate logs are used.
Every accepted slot writes an immutable, source-aware observation, even at the
same coordinates. The collector does not render or call Amazon Maps. DynamoDB
Streams triggers `suburban-remote-map-resolver` only for explicit `map-request#`
INSERTs created by authorized map reads. Observation writes, resolver status
updates and TTL deletions do not trigger rendering.

The resolver queries nine opaque spatial cells, each with at most 128 image
references, and checks exact Haversine distance. It selects the nearest compatible
background within 50 feet; a tie selects the newest generation. Fixed image
centres prevent cumulative drift. Image metadata and private S3 objects expire
30 days after successful generation. An unchanged observation also evaluates
expiry. Missing objects are misses. No cache hit makes an Amazon Maps request.

A fenced per-owner/vehicle generation lease prevents concurrent publication.
The observation is marked ready only after immutable object upload and readback.
Current delivery follows the newest observation identity, so completion of an
older observation cannot overwrite it. Observations retain their separate
90-day history expiry. Failed resolution is unavailable rather than fabricated.

The native Go renderer assembles Amazon Location `raster.satellite` tiles into
a lossless 3072x2048 PNG at effective 256-pixel Mercator zoom 19. It reads provider
attribution from `GetStyleDescriptor` and renders those credits in the footer;
it does not invent provider names or years. No vehicle marker is burned into
the background. Tile fetching and output size are bounded, and partial images
are rejected.

The explicitly negotiated large `/api/map/view` response contains one coherent
image reference, projection, source position, byte count, SHA256 and short-lived
download URL. The default V1 response instead includes the bounded thumbnail PNG;
its dimensions, projection zoom, notice rows and digest describe those exact
thumbnail bytes. Source coordinates, image identity and timestamps are unchanged.
The app validates the download and decodes it once. The 24MiB image never passes
through Lambda JSON. The app and widgets open at transform zoom 4, preserving
the old 768x512 ground extent, and can zoom out to 1 without another map request.
`/api/map/place` pin fractions continue to refer to the full image, not the
phone's initial cropped view. A renderer-version change invalidates old ready
image references as well as cache lookups.

Sources: [GetTile](https://docs.aws.amazon.com/location/latest/APIReference/API_geomaps_GetTile.html)
and [GetStyleDescriptor](https://docs.aws.amazon.com/location/latest/APIReference/API_geomaps_GetStyleDescriptor.html).

## Resolver recovery

The stream mapping reports partial batch failures, bisects failed batches, retries
at most twice, and stops at one hour of record age. Terminal event references go
to private queue `suburban-remote-map-resolver-dlq`, retained seven days. No raw
coordinates are logged. All function logs retain seven days. A generation lease
left by a terminated worker expires after two minutes; fix deterministic faults
before replaying an event. Replay is idempotent and never wakes the vehicle.

From an authorized GitHub workflow, replay one opaque observation key:

```bash
aws lambda invoke --function-name suburban-remote-map-resolver --cli-binary-format raw-in-base64-out --payload '{"kind":"replay-observation","observation":"location-observation#<opaque-id>"}' replay-result.json
```

The existing `location-map-refresh.yml` workflow replays the current stored
observation through the resolver, verifies the immutable PNG with OIDC, and
checks anonymous denial. No credential or source position belongs in a replay
argument. The workflow never treats a digest as a raw map key.

Vehicle observation time, successful collection time, and image generation time
remain distinct. Cache hits preserve generation time and update collection time.
IAM-only status exposes safe reasons, times, and an opaque image identifier;
zero time means unavailable. Live stream, provider projection, and owner/key
retrieval proofs remain required before delivery is declared complete.

Disable polling by setting `PollingEnabled=false` in samconfig.toml and deploying
through GitHub. The provider-paused flag also prevents scheduled provider calls.
