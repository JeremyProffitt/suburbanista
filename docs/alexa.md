# Alexa integration

Development skill only. US only. Not submitted for certification or publication. Skill ID lives in repo variable `ALEXA_SKILL_ID`.

Say Suburban. Full everyday phrases live in [alexa-readme.md](../alexa-readme.md). Source is [voice model](../skill-package/interactionModels/custom/en-US.json) and [handler](../internal/alexa/handler.go).

## What works today

- Status: fuel percent, miles to empty, total mileage, each with source age. GM data can be stale. The skill speaks the age.
- Lock, Unlock, Start: submits the command, replies sent, then check command status for the recorded result. Enabled and tested through mocked routing in run `40047681101`. No live actuation by the agent.
- Command status and connection status checks.
- Info reads: health, garage, details, plan, recalls, warranty, radio subscription. Answers depend on GM data and can be unavailable.
- Location is recognized but reported unavailable. No current fix is claimed.
- Previews: every preview starts with Demo and makes no broker call. Stop or Cancel ends the chat only.

Gated actions exist in the model but need separate capability proof: cancel remote start, trunk lock and unlock, alert sound, flash lights, stop alerts, stop lights. Trunk unlock is not powered liftgate proof. EV phrases are present but live EV support is not established for this truck.

## Build it

Owner workflow `Create Alexa skill identity` with inputs `resolve`, `update-icons`, `deploy-voice`, `configure-linking`, `inspect-linking`, plus `create` and `reference_skill_id`. It reuses a matching skill and creates only when absent. Voice deploy and readback passed in run `40047681101`.

```bash
gh workflow run alexa-identity.yml --ref main -f action=deploy-voice -f create=false -f reference_skill_id=<automatica-skill-id>
```

Icons publish from `https://suburban.jeremy.ninja/assets/`. Testing instructions stay development-only.

## Link it, still pending

Account linking is not configured yet. The plan reuses the one shared Login with Amazon profile from [lwa.md](lwa.md). Enter that same client ID and secret in the Alexa console. Add console-supplied Alexa return URLs to the LWA profile. `configure-linking` needs explicit `owner_approved_link_configuration=true` and passes the shared secret once. `inspect-linking` reads back without secrets.

Commands need the linked owner profile plus fresh server-side owner checks. A device-account token does not pass. Control flags in the State table default off. The `Owner command controls` workflow enables Start, Lock, and Unlock only after presence, outdoor location, and myChevrolet capability attestations.

## Screenshots to grab

- Developer console: skill ID, status development, endpoint, voice model build. Redact account IDs.
- Account linking page: auth and token URLs masked, scopes, return URLs partially masked.
- `artifacts/alexa-voice-40047681101/alexa-voice.json` and `artifacts/alexa-linking-40088618796/alexa-linking-inspection.json` as receipts.
- Phone Alexa app: skill enabled, account linked, sample `ask Suburban for status` result with ages visible.
- Lambda logs for status and connection checks with tokens and IDs redacted.
