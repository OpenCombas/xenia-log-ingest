# xenia-log-ingest

Xenia-Webservices log-ingestion backend.

```
Xenia client --(gzip)--> Xenia-WebServices /logs proxy --(stream + Bearer)--> xenia-log-ingest --> Loki --> Grafana
```

## Endpoints

- `GET /health` — unauthenticated liveness (the proxy can use it for its 503 gate).
- `POST /ingest` — the proxy forwards the upload here.
  - **Auth:** `Authorization: Bearer <INGEST_TOKEN>` (must equal the proxy's `LOG_BACKEND_TOKEN`). 401 otherwise.
  - **Body:** raw gzip (the same bytes the client uploaded). Metadata in `X-Xenia-*` headers (xuid, gamertag,
    note — URL-decoded; log-bytes, truncated, time). The log's first line (`Build: ...`) is indexed.
  - **Response:** `201 {"id":"<report id>","lines":<n>}` — the tester only ever gets the **id**. The Grafana
    deep-link is **operator-facing**: it's written to the ingest's server log, never returned to the client
    (so testers can't reach the internal log viewer or each other's logs).

## Loki model (low-cardinality by design)

Only **`job`** is a Loki **label** (single stream — cheap index). Every per-report identifier —
`report_id`, `xuid`, `gamertag`, `build`, `truncated`, `note` — rides as **structured metadata** on each
line (Loki 3.0+), so you filter in Grafana with e.g. `{job="xenia-logs"} | report_id="abc123"` **without**
exploding label cardinality. Lines are pushed in batches (`BATCH_LINES`), timestamped `X-Xenia-Time + line
index` (ns) so order is preserved.

Old Loki (< 3.0, no structured metadata): set `STRUCTURED_METADATA=false` — the report id is prefixed onto
each line instead (`report_id=<id> <line>`) so it stays greppable.

## Config (env)

| var | default | notes |
|---|---|---|
| `LISTEN_ADDR` | `:8090` | |
| `LOKI_URL` | — (**required**) | e.g. `http://loki:3100` |
| `INGEST_TOKEN` | — (**required**) | must match the proxy's `LOG_BACKEND_TOKEN` |
| `JOB_LABEL` | `xenia-logs` | the sole Loki label |
| `GRAFANA_BASE_URL` | — | if set, builds the `url` in the response (adjust the Explore link per your Grafana) |
| `BATCH_LINES` | `2000` | lines per Loki push |
| `MAX_UNCOMPRESSED_MB` | `1024` | backstop cap on gunzipped size (the proxy already caps the compressed upload) |
| `STRUCTURED_METADATA` | `true` | false = old-Loki fallback (prefix report id onto the line) |

## Run

```sh
go build ./... && LOKI_URL=http://localhost:3100 INGEST_TOKEN=dev ./xenia-log-ingest
# or
docker build -t xenia-log-ingest . && docker run -p 8090:8090 -e LOKI_URL=... -e INGEST_TOKEN=... xenia-log-ingest
```

Point the proxy at it: `LOG_BACKEND_URL=http://<ingest-host>:8090/ingest`, `LOG_BACKEND_TOKEN=<INGEST_TOKEN>`.

### Full stack (ingest + Loki + Grafana) via docker compose

`docker-compose.yml` brings up the ingest backend alongside a single-node **Loki** (store) and **Grafana**
(view), with the Loki datasource auto-provisioned:

```sh
cp .env.example .env          # set INGEST_TOKEN (must equal the proxy's LOG_BACKEND_TOKEN) + a Grafana password
docker compose up -d --build
```

- **Grafana** → http://localhost:3000 (login from `.env`). Query with `{job="xenia-logs"}`, filter a report
  with `| report_id="<id>"` (or `| xuid="…"`, `| build="…"` — all structured metadata).
- **ingest** → `:8090` (`/ingest`, `/health`); point the WebServices proxy's `LOG_BACKEND_URL` here.
- **Loki** → `:3100` (exposed for debugging; services reach it internally as `http://loki:3100`).

Loki uses the tsdb **v13** schema with `allow_structured_metadata: true` (config in `loki/loki-config.yaml`);
data persists in named volumes with ~31d retention. Bump the pinned Loki/Grafana image tags as desired.
