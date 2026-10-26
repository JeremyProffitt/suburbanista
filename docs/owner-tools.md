# Local owner tools

These Go tools use the existing owner HTTPS API at `https://suburban.jeremy.ninja`. They do not hold GM credentials. Windows protects the opaque owner session with current-user DPAPI in `%USERPROFILE%\.suburban-remote\owner-session.dpapi`. Do not copy that file into MCP configuration, logs, or the repository.

## Sign in

From `cmd.exe`, with the repository Go toolchain available:

```bat
"C:\dev\Suburban-Remote\scripts\owner-login.bat"
```

Open the printed public `http://127.0.0.1:<port>/start` address in the approved browser. Follow the owner sign-in link and complete consent yourself. The helper polls the existing mobile PKCE exchange and stores the session only after success. It expires after ten minutes. No authorization code, verifier, token, or private callback URL needs to be copied. Do not start another helper while a consent flow is pending.

## MCP

Build a local executable:

```bat
cd /d "C:\dev\Suburban-Remote"
go build -o dist\suburban-mcp.exe ./cmd/suburban-mcp
```

Configure an MCP client to launch `C:\dev\Suburban-Remote\dist\suburban-mcp.exe` with stdio transport, no arguments, and no credential environment variables. Standard output contains only MCP protocol messages. Alternatively, the user-run entry point is `scripts\suburban-mcp.bat`.

`vehicle_status` reads the owner dashboard. `latest_command_status` reads the latest command or the optional original `request_id`. Other `vehicle_<feature_id>` tools appear only when `/api/features` marks that feature available. Hyphens become underscores. Availability is checked again on every feature call. Read tools take an empty object.

Action tools operate the actual vehicle. They require a stable `request_id` of 16–128 letters, digits, underscores or hyphens. Optional action parameters follow the advertised schema. Reuse the same ID after an uncertain result. Never submit a new ID merely to retry. Read command status first when the outcome is unknown. Unavailable features, including unsupported EV features, are not tools and cannot execute. No action is needed to verify MCP installation: initialize, list tools, and read status only.

## Local report

After the trace-enabled backend is deployed and owner login succeeds:

```bat
"C:\dev\Suburban-Remote\scripts\onstar-report.bat"
```

This performs the eight catalog read operations, one per feature. It never starts, locks, or unlocks the vehicle. It writes `dist\onstar-report.html` and metadata-only `dist\onstar-fields.json`; both remain under the ignored `dist` directory. Open the HTML through the approved browser path.

The HTML contains only actual captured exchanges, with credentials, vehicle/account identifiers and precise location removed inside the worker. Oversized or non-JSON bodies are omitted. Missing responses are marked unavailable. Start, lock and unlock cards are explicitly non-executed templates with no invented provider answer. The metadata file contains safe source field names, units and presence flags, not readings.

The owner API accepts `POST /api/vehicle/read` with `featureId` and optional `trace: true`. Authentication, session revocation checks, provider lease, refresh persistence and read budgets remain in effect. Trace capture starts only after authentication; auth endpoints and actuation endpoints cannot be traced. Traces are returned only in the current response, never stored in snapshot documents. Ordinary MCP reads request no trace.

## Verification boundary

Unit tests exercise the real MCP SDK protocol against a synthetic owner API, not the owner's car. A successful local build or test does not prove live provider readiness. Record live authenticated status/report results separately after deployment and owner consent. Do not paste report contents or session data into an assistant conversation.
