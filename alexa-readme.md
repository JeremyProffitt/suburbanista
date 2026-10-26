# Alexa commands

Verified 2026-10-24 against the deployed voice model and Alexa handler. Say **Suburban** as the skill name. Link the owner's Amazon account in the Alexa app; voice identification is not required.

## Everyday commands

- **“Alexa, ask Suburban for status.”** Reports fuel percentage, estimated miles to empty, total mileage, and each reading's age. You can also ask for “vehicle status,” “fuel level,” “miles to empty,” or “total mileage.”
- **“Alexa, tell Suburban to lock.”** Submits a door-lock request. Normal reply: “Lock command sent.”
- **“Alexa, tell Suburban to unlock.”** Submits a door-unlock request. Normal reply: “Unlock command sent.”
- **“Alexa, tell Suburban to start.”** Submits a remote-start request. Normal reply: “Start command sent.”
- **“Alexa, ask Suburban for command status.”** Reports the recorded outcome of the latest command for the linked account.
- **“Alexa, ask Suburban for connection status.”** Checks owner authorization and enabled capabilities. This does not establish a live connection to the vehicle.

Start, lock, and unlock were verified enabled on 2026-10-24. “Command sent” acknowledges confirmed submission to the command service. Failed or uncertain requests receive the corresponding error response. Ask for command status to check the recorded result.

## Status limits

- GM can return older cached readings. Alexa states each reading's source age; a new API fetch does not make the reading current.
- Missing fuel, range, or mileage values are reported as unavailable.
- Current physical lock state and engine-running state are omitted. No verified live fields for those states were found in the examined diagnostics or passive digital-twin responses.
- Command history is not used to infer current lock or engine state.

## Other information requests

These request information. The answer depends on the data GM supplies and may be unavailable or stale.

- **“Alexa, ask Suburban to check vehicle health.”** Diagnostics summary.
- **“Alexa, ask Suburban to show my garage.”** Enrolled vehicle information.
- **“Alexa, ask Suburban to get vehicle details.”** Vehicle details and reported command eligibility.
- **“Alexa, ask Suburban to check my on star plan.”** OnStar plan information.
- **“Alexa, ask Suburban to check recalls.”** Returned recall information.
- **“Alexa, ask Suburban to check warranty.”** Warranty information.
- **“Alexa, ask Suburban to check radio subscription.”** SiriusXM subscription information.

**“Alexa, ask Suburban to check location.”** is recognized, but a current location fix is not verified. The skill currently reports location as unavailable rather than speaking an address or coordinates.

## Gated vehicle actions

The voice model recognizes these phrases, but these actions were not available at the last capability check. They require separate capability verification and enablement; they are not advertised as ready for use.

- “Alexa, tell Suburban to cancel remote start.”
- “Alexa, tell Suburban to lock the trunk.”
- “Alexa, tell Suburban to unlock the trunk.”
- “Alexa, tell Suburban to sound the alert.”
- “Alexa, tell Suburban to stop the alerts.”
- “Alexa, tell Suburban to flash the lights.”
- “Alexa, tell Suburban to stop flashing the lights.”

Trunk unlock does not establish support for powered liftgate movement. “Stop the engine” is another phrase for canceling remote start; it is subject to the same availability check.

## Help and previews

- **“Alexa, open Suburban.”** Opens the skill and offers status, lock, or unlock.
- **“Alexa, ask Suburban for help.”** Explains the main requests.
- **“Alexa, ask Suburban for feature catalog.”** Lists feature groups.
- **“Alexa, ask Suburban to preview vehicle health.”** Speaks a clearly labeled example without contacting the vehicle.
- **“Alexa, stop.”** Ends the conversation. “Cancel” also ends it; neither stops the engine.
- While the skill is listening, **“go home”** returns to the help response.

Other preview names include remote start, door lock, door unlock, engine stop, cargo lock, cargo unlock, horn and lights, stop alerts, flash lights, stop lights, garage, vehicle details, on star plan, recalls, warranty, radio subscription, location, charge target, stop charging, charging metrics, and refresh charging metrics.

## EV phrases: live operation unavailable

These phrases exist in the model, but live EV support and its authorization are not established for this Suburban. Use previews for examples.

- “Alexa, tell Suburban to set charge target to 80 percent.”
- “Alexa, tell Suburban to stop battery charging.”
- “Alexa, ask Suburban to check charging metrics.”
- “Alexa, tell Suburban to refresh charging metrics.”

## Verification and source

Status and connection status were tested through the computer speaker and verified in Lambda logs. Lock, unlock, and start routing and exact acknowledgements passed automated tests with a mocked command service; the agent did not execute vehicle-control commands.

- [Voice model](skill-package/interactionModels/custom/en-US.json)
- [Alexa handler](internal/alexa/handler.go)
- [Live-state API findings](docs/research/alexa-live-status.md)

Feature availability can change. “Connection status” checks the current service configuration; it does not prove every feature is supported or that the vehicle is online.
