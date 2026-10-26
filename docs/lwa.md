# Login with Amazon integration

One shared Login with Amazon profile serves web, mobile, MCP, and Alexa. No second profile. No Alexa-specific client ID. Source flow lives in `scripts/secretctl.bat lwa` and receipts in `artifacts/`.

## Profile shape

- One security profile for all four surfaces.
- Web callback registered on the profile for the owner API and app PKCE flow.
- Alexa return URLs added later from the Alexa console. They are allowlist entries only.
- Client type is confidential. The secret stays server side in the API and worker plus Alexa account linking. Never in the skill Lambda, ASK job, APK, or logs.

## Prove the owner once

`secretctl.bat lwa` uses code authorization with PKCE, verifies the owner in the browser, then stores `LWA_CLIENT_ID`, `LWA_CLIENT_SECRET`, and `OWNER_AMAZON_USER_ID`. The baseline callback shows an expected Unrecognized login state page. Paste its full address into the hidden prompt. Nothing secret prints.

`secretctl owner-id` re-proves against the same profile and prints only the owner ID. It changes no binding.

## How sign-in flows

- App: Sign in with Amazon, browser completes consent, return to app. Session is revocable and encrypted. Android Keystore guards the saved session and pending request.
- MCP at `/mcp`: Streamable HTTP with OAuth discovery and dynamic registration. S256 PKCE, exact return URLs, one-use codes, resource-bound tokens with rotation. Amazon credentials stay server side.
- Owner tools: `scripts/owner-login.bat` polls the mobile PKCE exchange for ten minutes and stores the session under DPAPI. Used by the HTML exporter and local report only.
- Alexa: same client ID and secret entered in the Alexa account-linking console. Device token alone never passes command checks. See [alexa.md](alexa.md).

## Map key beside LWA

LWA owner sessions read the cached map free. Keyed embeds use a private `key` query value for sharing. Anyone holding the URL can read the image. Never paste it in chat, logs, source, screenshots, email, or public pages. Setup and revoke live in [setup.md](setup.md) and [location-map.md](location-map.md). Store raw value only in SSM `suburban-map-key`. GitHub holds only `MAP_READ_KEY_SHA256`.

## Screenshots to grab

- LWA console: profile, client ID partially masked, auth and token endpoints, web callback list redacted.
- Alexa return URLs added to the same profile, values partially masked.
- Helper run showing owner verified with no secret printed.
- App sign-in screen, browser consent, and return-to-app state with IDs redacted.
- Map verify helper showing HTTP codes for valid, missing, wrong, and scope-denied keys, with no raw key on screen.
