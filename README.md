# ArahRide — Demo Go Server

This local server connects Arah Customer and Arah Driver apps on Android and iOS. It is **not a production backend**: it supports one Driver and one active order, keeps state in process memory, and has no accounts or authorization. By default, it listens only on 127.0.0.1:18080.

## Architecture

~~~mermaid
flowchart LR
  C[Customer app] -->|HTTP + WebSocket| API[Go API net/http]
  D[Driver app] -->|HTTP + WebSocket| API
  API --> HUB[In-memory hub<br/>dispatch · timer · presence · WS fan-out]
  HUB --> OSRM[OSRM-compatible routing]
  HUB --> GEO[Photon / Nominatim geocoding]
~~~

Clients send ride actions over HTTP (/v1/quotes and /v1/actions) and receive live snapshots over WebSocket (/v1/events). Dispatch, timers, presence, and fan-out all run in a single process.

## Tech stack

- **Language:** Go 1.23
- **HTTP:** net/http (standard library)
- **WebSocket:** github.com/coder/websocket
- **Routing:** OSRM-compatible (router.project-osrm.org by default)
- **Geocoding:** Photon / Nominatim (ARAH_GEOCODER_URL)
- **State:** In-memory hub in one process (no database)

## Run the server

Requires Go 1.23 or later (brew install go on macOS).

~~~sh
go mod download
go run .
~~~

For a physical device on a development LAN:

~~~sh
ARAH_ADDR=0.0.0.0:18080 go run .
~~~

Do not expose the demo server port to the internet. To verify it, run go test ./... and curl http://127.0.0.1:18080/health.

## Prototype API

- GET /v1/state → the current snapshot.
- POST /v1/actions → one action with JSON role, action, and an optional point.
- GET /v1/events?role=customer|driver → a WebSocket that sends the initial snapshot and each subsequent change.

Statuses: requested, accepted, arrived, in_progress, completed, cancelled.
Customer actions: create (requires pickup and dropoff), cancel, reset.
Driver actions: online, offline, accept, arrive, start, complete, location (requires location).
Points use JSON {"latitude":-6.2088,"longitude":106.8456}. A snapshot contains sequence, driverOnline, driverLocation, and ride (or null). The sequence lets apps ignore stale updates when HTTP and WebSocket messages arrive out of order.

When a Customer places an order, the server retrieves road geometry from OSRM (ARAH_ROUTING_URL, default https://router.project-osrm.org) and stores it with the ride. The Driver start action triggers simulated location updates every 100 ms for 16 seconds along the route geometry. Both apps draw the same geometry as a polyline and move the car icon along it. The apps pause the Driver device's GPS updates while the ride is in_progress so they do not overwrite the simulation.

The public OSRM endpoint is for demos only and may limit usage. For production, host OSRM yourself or choose a routing service with an SLA; pickup and destination locations are sent to the configured routing endpoint.

## Place search (Photon)

- GET /v1/places/search?q=query&lat=optional&lon=optional → {"results":[{"name","displayName","point"}]}. Query length must be 2–160 characters; at most 8 results are returned; lat and lon are optional but must be provided together.
- GET /v1/places/reverse?lat=latitude&lon=longitude → one PlaceResult; returns 404 when there is no match.
- Invalid input → 400; upstream timeout (4 seconds), non-2xx response, or invalid JSON → 502 with a generic message (the upstream response body is not forwarded).
- ARAH_GEOCODER_URL sets the Photon base URL. The default, https://photon.komoot.io, is a public demo endpoint for development only. Production requires a managed or self-hosted Photon service because search terms and map points are sent to that endpoint.

- ARAH_GEOCODER_PROVIDER selects the provider: photon (default) or nominatim. With nominatim, ARAH_GEOCODER_URL defaults to https://nominatim.openstreetmap.org (development only; its usage policy limits requests to one per second and requires a User-Agent, both of which the backend provides). Results are limited to Indonesia, and RT/RW/postal-code labels are removed. The public Photon API (photon.komoot.io/api) may not respond; use ARAH_GEOCODER_PROVIDER=nominatim for development or run your own Photon service.

Nominatim example:

~~~sh
ARAH_GEOCODER_PROVIDER=nominatim go run .
~~~

Local self-hosted Photon example:

~~~sh
ARAH_GEOCODER_URL=http://127.0.0.1:2322 go run .
~~~

Example flow without the apps:

~~~sh
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"driver","action":"online"}'
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"customer","action":"create","pickup":{"latitude":-6.2088,"longitude":106.8456},"dropoff":{"latitude":-6.1754,"longitude":106.8272}}'
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"driver","action":"accept"}'
~~~

## Production recommendations for the Go backend

Use **HTTPS for actions and WebSocket/WSS for live ride updates to apps**. A WebSocket is a direct two-way connection to a device; RabbitMQ is a service-to-service broker, not a replacement for a real-time connection to an app. While a single Go process is sufficient, a local broadcast like this demo is the simplest option. When running multiple instances, use Redis Pub/Sub or a similar broker to fan out across gateways; store ride state and the latest location in PostgreSQL/PostGIS so reconnecting clients can retrieve a snapshot. If asynchronous jobs need retries and durable queues later (for example, notifications, ride receipts, or analytics), RabbitMQ is a good fit.

Before release, add Customer/Driver authentication, per-ride and per-channel authorization, race-safe Driver selection, idempotency keys for actions, durable state storage, location validation and rate limits, WebSocket heartbeat/reconnect handling, APNs/FCM push notifications when the app is inactive, TLS, observability, a location-retention policy, and separation between demo and production environments. The current demo endpoints do not meet these requirements.

## Place search verification results (2026-10-15)

- Automated: Go: go test ./... passed.
- With a local mock Photon server (ARAH_GEOCODER_URL), /v1/places/search and /v1/places/reverse mapped results correctly; an empty query returned 400.
- **Not verified:** The public Photon service (photon.komoot.io) was unreachable during testing (the backend returned a generic 502), so real Indonesian address/POI search and production availability were not tested. End-to-end emulator/simulator testing (select a point, create an order, show the label in the Driver app, and tap the map) has not been run.
