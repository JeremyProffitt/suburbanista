# Alexa live vehicle status

Researched 2026-10-24. This researcher made no authenticated GM request, vehicle read, wake request, or vehicle command. The parent run supplied the separate passive-read evidence below.

## Result

**Current physical lock state and engine-running/ignition state are not verified capabilities of this integration.** No corresponding read method, response field, or freshness contract was found in the pinned/current OnStarJS source or the five reviewed ICE diagnostic fixtures. This is an evidence limit, not proof that GM cannot supply these signals through another API. Do not announce locked/unlocked or running/stopped from command history. [Request implementation](https://github.com/BigThunderSR/OnStarJS/blob/1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d/src/RequestService.ts), [response types](https://github.com/BigThunderSR/OnStarJS/blob/1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d/src/types.ts)

The public master tree resolved to `1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d`, the existing pin. Downloaded current and pinned `RequestService.ts` contents were equal. [Current tree](https://api.github.com/repos/BigThunderSR/OnStarJS/git/trees/master?recursive=1), [local parity record](../../internal/onstar/parity.json)

## Read endpoints and exact fields

The diagnostic read is `GET https://na-mobile-api.gm.com/api/v1/vh/vehiclehealth/v1/healthstatus/{VIN}`. Upstream does not request a live probe or poll this endpoint. Its generic schema contains `diagnostics[].diagnosticElements[]` with `name`, `value`, `uom`, and optional `cts`. [RequestService.ts:526](https://github.com/BigThunderSR/OnStarJS/blob/1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d/src/RequestService.ts#L526), [types.ts:251](https://github.com/BigThunderSR/OnStarJS/blob/1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d/src/types.ts#L251), [local client](../../internal/onstar/owner_reads.go)

The same maintainer's downstream ICE fixtures give concrete names:

- Fuel percentage: diagnostic `FUEL_LEVEL_STATUS`, element `FUEL_LEVEL`, unit `%`.
- Fuel range: diagnostic `FUEL_LEVEL_STATUS`, element `FUEL_RANGE`, unit `KM`; some responses also have `FUEL_RANGE_MI`, unit `MI`.
- Odometer: diagnostic `ODOMETER`, element `ODO_READ`, unit `KM`; some responses also have `ODO_READ_MI`, unit `MI`. `TRIP_A_ODO` is a separate trip counter.

Use each element's own `value`, `uom`, and `cts`. These fixtures represent other vehicles. [Pinned ICE fixture 3](https://github.com/BigThunderSR/onstar2mqtt/blob/10aef9caff47bfc8edd83ee09b14f6bde86916da/test/diagnostic-sample-v3-ice-3.json)

Values can be absent. Child timestamps can be much older than the response timestamp. Fixture 5 has root time `2026-09-28T01:15:15.001Z`, but fuel percentage time `2026-09-27T21:22:04.049Z`. A newly fetched response is not necessarily a current vehicle reading. [Missing-value fixture](https://github.com/BigThunderSR/onstar2mqtt/blob/10aef9caff47bfc8edd83ee09b14f6bde86916da/test/diagnostic-sample-v3-ice-2.json), [timestamp fixture](https://github.com/BigThunderSR/onstar2mqtt/blob/10aef9caff47bfc8edd83ee09b14f6bde86916da/test/diagnostic-sample-v3-ice-5.json)

## Passive owner evidence

The parent run reported a successful existing `Read(diagnostics,false)` and cached `/api/vehicle/read/status` inspection, with no actuation or wake. Fetch time was `2026-10-24T13:19:24.178505516Z`; provider report time was `2026-10-23T19:08:03.001Z`. Actual fields included `FUEL_LEVEL/%` at `2026-10-23T18:46:42.009Z`, and `FUEL_RANGE/KM` plus `ODO_READ/KM` at `2026-10-23T19:08:03.001Z`. These readings were stale. No lock, ignition, or running field appeared among the returned diagnostic value descriptors. Engine-related descriptors described type, filter health, and power loss. Values are omitted here; the parent run owns verification evidence in the [execution plan](../../plan.md).

## Limits that affect Alexa

Upstream location calls `GET /veh/datadelivery/digitaltwin/v1/vehicles/{VIN}?sms=true&region=na`, then polls with `sms=false` while `telemetry.data.session.updatePending` is `PENDING`. This establishes a location-refresh procedure, not lock or engine-state fields. `IgnitionOn` is an alert-command override, not a read result. [Location and action source](https://github.com/BigThunderSR/OnStarJS/blob/1ae0cbb6fd7601e871b67cd0cda02c80c4354f9d/src/RequestService.ts#L780)

Command submission, command completion, and present physical state are different facts. The local command client parses execution status. A completed lock or remote-start request does not prove the vehicle stayed in that state. [Local command client](../../internal/onstar/commands.go)

Chevrolet's app guide lists fuel, odometer, and door/hood/trunk **open** status where applicable, plus last-update time. Open/closed does not mean locked/unlocked. No lock-state or engine-state API mapping is supplied. [Official app instructions](https://www.chevrolet.com/support/vehicle/smartphone-connections/mychevrolet-mobile-app/instructions)

GM's public FAQ places detailed technical documentation behind a commercial partner agreement; it supplies no such read contract for this mobile client. [GM developer FAQ](https://developer.gm.com/faqs)

Implementation rule: report available diagnostic numbers with their provider observation time and age. Missing or invalid timestamps mean unknown freshness. Keep live lock and engine state unavailable until a field, meaning, and freshness can be verified for this vehicle. Fetch time is separate from observation time. [Snapshot code](../../internal/onstar/snapshots.go)

## Passive digital-twin evidence

The parent run verified that `ReadSnapshot` calls `c.Location(ctx, token, vin, false)`. Its existing `ownerclient.Read(location,true)` enabled tracing; the boolean did not enable wake. The observed request was `GET /veh/datadelivery/digitaltwin/v1/vehicles/[vehicle]?sms=false&region=na`, with HTTP `200` and trace `truncated=false`. No wake or actuation occurred. [Snapshot call](../../internal/onstar/snapshots.go), [location client](../../internal/onstar/readonly.go), [parent execution evidence](../../plan.md)

The parent inspected only redacted property names. The response exposed `response.telemetry.{lastUpdateId,lastUpdateSrc,occurredTs,processingTs,data}` and, under `data`, `position`, `session.updatePending`, `velocity.spdInKph`, and `velocity.dir`. It also contained `response.operation` metadata and `response.vehicle` identity field names. Identity and coordinate values were not read or shown. The `position` subtree was intentionally not inspected.

No lock, ignition, or engine-running property appeared in the examined response shape. This is vehicle-specific evidence for the inspected cached response, not proof of absence from every GM API or from an uninspected subtree. [Parent execution evidence](../../plan.md)
