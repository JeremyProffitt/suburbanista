# Cost

Owner-only truck remote, so idle cost stays low. One vehicle, one owner, tiny traffic. Numbers below are planning ranges for `us-east-1`, not a bill. Verify with Cost Explorer filtered by `Project=Suburban-Remote`.

```bash
aws ce get-cost-and-usage --time-period Start=2026-10-01,End=2026-11-01 --granularity MONTHLY --metrics BlendedCost --group-by Type=DIMENSION,Key=SERVICE
aws logs describe-log-groups --query "logGroups[?starts_with(logGroupName,'/aws/lambda/suburban')].[logGroupName,retentionInDays]"
```

## What runs

From `template.yaml` and `samconfig.toml`:

- 4 arm64 Lambdas on `provided.al2023`, 256 MB each: Api 30s reserved 5, Worker 120s, MapResolver 60s reserved 1, Alexa 7s reserved 2.
- HttpApi with custom domain, Route53 zone, ACM cert.
- DynamoDB StateTable on demand with streams and TTL,Deletion protection on.
- S3 MapBucket private with 30-day `map-cache/` expiry, plus release and SAM buckets.
- SQS WorkQueue FIFO plus Worker, Scheduler, and MapResolver DLQs.
- Scheduler `cron(0,30 * * * ? *)`, 1,440 runs per month, currently `PollingEnabled=true` in `samconfig.toml`.
- 4 CloudWatch LogGroups at 7-day retention.
- Amazon Location maps `GetStaticMap` plus `GetTile`, SSM `suburban-map-key`, SES status mail, self-hosted GitHub runner with 7-day artifacts.

## Monthly range

- Quiet with polling on: 4 to 11 USD.
- Quiet with polling off: 2 to 6 USD.
- Heavy screenshot or proof week: add 1 to 5 USD for Location renders, logs, and API calls.

Largest to smallest: Route53 zone about 0.50 plus queries, Location map renders, Lambda GB seconds and HttpApi calls, DynamoDB on demand plus streams, S3 storage and requests, CloudWatch ingest and storage, SQS and Scheduler near zero, SSM standard parameter free, ACM free, SES pennies, self-hosted runner power only.

Why this stays cheap: arm64, 256 MB functions, reserved concurrency caps, no DynamoDB Scan on serving paths, 7-day logs, 30-day map expiry, 1,440 capped polls, cached 768 by 512 PNG reused instead of re-render.

## Watch items

- Location renders cost each poll that rebuilds the PNG. Cached reads cost almost nothing. Keep single-image cache, keep manual refresh coalesced, keep Overpass and debug renders out of prod.
- Logs are the silent riser. JSON INFO on four functions is fine. Do not raise to DEBUG in prod. Keep 7 days.
- Streams plus MapResolver fire per map-request insert only. Fine for one truck. Revisit if polling fans out.
- DLQs retain 7 days and 1 day on work queue. Fine. Purge poison messages instead of growing retention.
- Release APKs and deployment bundles live in S3 and Actions artifacts. Keep 7-day artifact retention. Prune old release binaries deliberately, never auto-delete the signed prod candidate.
- No CloudWatch alarms or dashboards in the template. Do not add them. They bill monthly whether they fire or not.

## Cut it further

- Turn `PollingEnabled=false` when parked for weeks, deploy, then re-enable before trips. Saves Location and Worker cost.
- Lengthen poll from 30 to 60 minutes if stale cues stay acceptable. Halves poll cost.
- Keep Satellite static 19 with fixed 768 by 512. Larger or fresher tiles cost more with no driver gain.
- Keep on-demand DynamoDB. Provisioned only wins with steady high traffic this truck will never reach.
