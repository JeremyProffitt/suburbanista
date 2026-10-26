# Static map provider options

Research date: 2026-10-24. Documentation and published pricing only; no account was created, provider selected, subscription purchased, or map request made. Implementation and deployment remain paused.

## Fit for this application

The required design generates one image on the backend, stores it privately in S3, and serves it repeatedly through the authenticated app/keyed URL. The nominal ceiling is 48 generations/day or 1,488 in a 31-day month, assuming coordinates change every scheduled slot. Retries and tests would be additional requests.

Research shortlist, not a selection:

- **Geoapify:** strongest low-volume candidate. Its first-party static-map page expressly permits storing and caching static maps and serving cached images with attribution. The free plan may cover this volume, depending on image/marker credits. [Static maps](https://www.geoapify.com/static-maps-api/), [pricing](https://www.geoapify.com/pricing/).
- **Stadia Maps `static_cacheable`:** clearest explicit paid license for serving saved images from our own infrastructure. Its cacheable-image credit cost is materially higher than its ordinary static endpoint. [Current terms, §30.3](https://stadiamaps.com/terms-of-service/versions/2026-03-18/), [pricing](https://stadiamaps.com/pricing/).

The rankings are engineering inferences from the cited documentation, not legal clearance or an availability guarantee. A static-image API and HTTP cache headers alone do not establish permission for persistent S3 storage and repeated private redistribution.

## Geoapify

- **Image/authentication:** Static Maps API returns PNG or JPEG. A Geoapify account and API key are required. [API reference](https://apidocs.geoapify.com/docs/maps/static/).
- **Storage and private reuse:** The first-party static-map page explicitly allows storing and caching maps and serving cached images, with required attribution retained. This is the closest documented match for the existing S3/app/widget design. [Storage and caching policy](https://www.geoapify.com/static-maps-api/).
- **Low-volume price:** Free plan: 3,000 credits/day, no credit card required. Static-map charges combine one request credit, map-tile credits, and marker credits. The published 800×600/six-marker example costs nine credits; 48 such images would use 432 credits/day. This is an illustration, not an exact quote for our map. [Pricing](https://www.geoapify.com/pricing/), [static-image credit calculation](https://apidocs.geoapify.com/docs/maps/static/#pricing).
- **Before choosing:** Confirm the final style, output size, scale and marker credit count. Keep OSM/style-provider attribution and the extra Geoapify attribution required on the free plan visible in the image. No latency/reliability claim has been tested. [Attribution requirements](https://apidocs.geoapify.com/docs/maps/static/#attribution).

## Stadia Maps

- **Image/authentication:** PNG and JPEG are supported. Backend requests require an account and API key; domain authentication is another option for web clients. [Static Maps documentation](https://docs.stadiamaps.com/static-maps/).
- **Storage and private reuse:** Use `static_cacheable`, not the ordinary `static` endpoint. The current addendum permits saved/cached images and redistribution through the customer's systems while an active paid subscription is maintained. Ordinary static images do not grant that server-cache exception. [Current terms, §30.3.3](https://stadiamaps.com/terms-of-service/versions/2026-03-18/), [endpoint distinction](https://docs.stadiamaps.com/static-maps/).
- **Low-volume price:** Cacheable images cost 2,000 credits/request. Starter is $20/month with 1,000,000 credits and $0.03/1,000 extra credits. At 1,488 images, that is 2,976,000 credits: an inferred $79.28/month before tax and other usage. Standard is $80/month with 7,500,000 credits. [Published credit schedule and plans](https://stadiamaps.com/pricing/).
- **Before choosing:** Confirm paid-plan availability and attribution for the selected style. At unchanged coordinates, generation count can be much lower. The site announces revised terms effective November 9, 2026; recheck them before later activation. [Current terms and announced revision](https://stadiamaps.com/terms-of-service/).

## Mapbox

- **Image/authentication:** Static Images API supports PNG, JPEG and WebP; requires a Mapbox access token. [API reference](https://docs.mapbox.com/api/maps/static-images/).
- **Low-volume price:** The first 50,000 Static Images API requests/month are free; the next tier is $1 per 1,000. The nominal generation count fits that free allowance, but price does not resolve caching rights. [Static Images pricing](https://www.mapbox.com/pricing#static-images-api).
- **Storage and private reuse:** Current Product Terms §2.8.1 permit limited on-device caching populated directly from Mapping APIs and restrict redistribution from caches/proxies. This is not a default fit for backend generation followed by S3 redistribution. Seek written terms for that architecture. [October 20, 2026 Product Terms, §2.8.1](https://cdn.prod.website-files.com/609ed46055e27a02ffc0749b/6ad79764a359d21b46049ea4_Mapbox%20Product%20Terms%20%28October%2020%2C%202026%29.pdf), [official legal portal](https://www.mapbox.com/legal/product-terms).
- **Important distinction:** The API's cache-control durations describe technical cache behavior. They do not override the product terms or authorize an S3 image service. [Static Images caching documentation](https://docs.mapbox.com/api/maps/static-images/#static-images-api-restrictions-and-limits).

## Google Maps Static API

- **Image/authentication:** PNG is supported. A Google Cloud project with billing enabled and an API key or supported OAuth authentication is required; Google recommends signing static-map URLs. [Image parameters](https://developers.google.com/maps/documentation/maps-static/start), [usage and billing](https://developers.google.com/maps/documentation/maps-static/usage-and-billing).
- **Low-volume price:** Static Maps has a 10,000-event monthly free cap; the first paid tier is $2 per 1,000. The nominal 1,488 generations would be inside the cap, subject to account usage. [Current pricing list](https://developers.google.com/maps/billing-and-pricing/pricing).
- **Storage and private reuse:** Google's FAQ expressly says not to store and serve copies of Maps Static images from a website; pages must reference Google directly. Private-access applications are permitted, but that does not authorize our S3 reuse. This needs a different delivery design or explicit provider agreement. [Official FAQ: stored static images and private access](https://developers.google.com/maps/faq?hl=en#staticmap_reuse).

## MapTiler Cloud

- **Image/authentication:** Static Maps API supports PNG, JPEG and WebP with a MapTiler API key. [Cloud API reference](https://docs.maptiler.com/cloud/api/static-maps/).
- **Low-volume price, clarified:** Static Maps requires a paid plan. Flex is $30/month with 500,000 API requests included. Each static image counts as 15 requests: 1,488 images use 22,320 requests, so the base estimate is **$30/month**, assuming no other usage consumes the allowance. [Paid-plan requirement](https://docs.maptiler.com/cloud/api/static-maps/), [15-request multiplier](https://docs.maptiler.com/guides/account/sessions-vs-requests/), [plan allowance and price](https://www.maptiler.com/cloud/pricing/).
- **Price limitation:** The $30 estimate covers ordinary API access, not a custom S3-caching/proxy license. That agreement's price remains unquoted. Published request overage is $0.15 per 1,000 billable requests. [Pricing](https://www.maptiler.com/cloud/pricing/), [custom-use terms](https://www.maptiler.com/terms/cloud/).
- **Storage and private reuse:** Cloud terms prohibit server-side caching/redistribution by default and place proxy/export use under a custom written agreement. Client-device caching is a separate permission. Do not use the standard plan as authorization for private S3 redistribution. [Cloud terms, §§5–7](https://www.maptiler.com/terms/cloud/).
- **Scope:** This comparison concerns hosted MapTiler Cloud. Self-hosted MapTiler Server is a different product and operating commitment, not a drop-in hosting change authorized by this research. [Server Static Maps API](https://docs.maptiler.com/server/api/static-maps/).

## Amazon Location Service

- **API exists/authentication:** `GetStaticMap` exists and accepts an API key or valid SigV4 signature. An AWS account is required; a Lambda IAM role could provide server authentication without another provider API key. [API reference](https://docs.aws.amazon.com/location/latest/APIReference/API_geomaps_GetStaticMap.html).
- **PNG uncertainty:** The API reference describes the response as JPEG and exposes no image-format selector. The developer guide inconsistently mentions JPEG-only delivery and JPEG-or-PNG responses. Native PNG output is therefore unconfirmed. Conversion might be technically possible, but would not itself grant storage rights. [API response](https://docs.aws.amazon.com/location/latest/APIReference/API_geomaps_GetStaticMap.html), [developer guide](https://docs.aws.amazon.com/location/latest/developerguide/static-maps.html).
- **Low-volume price, clarified:** The official us-east-1 price list specifies **$0.0005 per GetStaticMap request ($0.50 per 1,000)**. At 1,488 requests, the calculated service charge is **$0.744, about $0.74/month**, before tax and other AWS costs. No free-tier or credit assumption is used. [Official regional price-list JSON](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonLocationService/20260911124449/us-east-1/index.json).
- **Rate verification:** Public catalog publication: September 11, 2026; effective rate date: April 1, 2026. The matching product is `GetStaticMap`, provider `Default`, region `us-east-1`, type `Static map request`. Eligibility for the pricing page's trial/credits was not checked. [Regional catalog index](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonLocationService/current/region_index.json), [free-tier terms](https://aws.amazon.com/location/pricing/).
- **Storage and private reuse:** AWS Service Terms §82.4 restrict storage/caching for non-Open-Data providers, with named exceptions that do not establish this static-image reuse. `GetStaticMap` lists Standard/Satellite styles. Confirm the applicable data-provider license and S3/widget redistribution rights with AWS; AWS hosting alone is insufficient. [Service terms, §82](https://aws.amazon.com/service-terms/#82._Amazon_Location_Service), [supported styles](https://docs.aws.amazon.com/location/latest/APIReference/API_geomaps_GetStaticMap.html).

## Required checks before any integration

- Obtain the owner's provider choice and any purchase approval. Research does not authorize signup, keys, charges, code changes or deployments.
- Confirm persistent private S3 storage, repeated authenticated app/widget display, image retention while stationary, and any requested email attachment are covered. Preserve required attribution when adding the telemetry-age label or resizing.
- Keep the upstream provider credential separate from `suburban-map-key`, which authorizes retrieval from our application. Never put either credential or a key-bearing URL in logs, chat or committed files.
- A provider request would disclose its requested map area. Review the selected provider's retention/privacy terms before sending real vehicle coordinates. No actual location was supplied during this research.
- No availability benchmark or live-map generation was performed. Existing implementation, release and installation holds remain in effect.

## Research notes

All cited pages were opened directly. The current Mapbox legal portal's PDF could not be parsed by the browsing tool, so its first-party PDF was downloaded and read locally; §2.8.1 was checked. No claim relies on a search snippet, forum answer, or an old Mapbox terms PDF.

Cost follow-up on the same research date: AWS's public regional price-list JSON was read directly over HTTPS without AWS account credentials. The exact `GetStaticMap` product and its on-demand rate were matched. MapTiler's first-party request accounting documentation confirmed the 15-request image multiplier. Neither check invoked a map API.

Pricing is the public USD schedule observed on the research date, not a purchase quote. Estimates exclude existing AWS storage/transfer costs, taxes, other account usage, retries and optional services. Unverified or conflicting items are explicitly marked rather than inferred from a product's existence.
