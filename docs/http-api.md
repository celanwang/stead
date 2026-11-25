# Stead HTTP API

Start the API and worker with [Compose](operations.md), or run `stead migrate`, `stead serve` and `stead worker` with the same `STEAD_MYSQL_DSN`. The server accepts an optional certificate and key with `-tls-cert` and `-tls-key`. Behind a reverse proxy, terminate TLS at the proxy and keep the service listener private.

Use the bearer token configured for the principal in `STEAD_API_TOKENS`. Stead derives the owner or reviewer from that token. An unrelated principal receives `404` for another owner's delegation. The reviewed-draft endpoint is optional and sends the prompt to the configured model. It writes no delegation or authority.

| Method and path | Operation |
| --- | --- |
| `GET /healthz` | Process liveness, public |
| `GET /readyz` | MySQL connectivity and migration checksums, public |
| `GET /v1/session` | Identify the principal bound to this token |
| `POST /v1/drafts` | Interpret JSON `{"text":"..."}` as a draft or request clarification |
| `POST /v1/delegations` | Submit a scheduling or briefing delegation with an idempotency key |
| `GET /v1/delegations?after=...` | Page through the current principal's delegations |
| `GET /v1/delegations/{id}` | Read delegation status, grants, actions and receipts |
| `POST /v1/delegations/{id}/events` | Record an owner event or a designated reviewer's review |
| `GET /v1/reviews/{id}` | Read eligible source fingerprints and shareable excerpts as the designated reviewer |
| `GET /v1/projects/{project}/feed` | Read up to 100 unread notifications |
| `POST /v1/projects/{project}/feed/{seq}/ack` | Acknowledge one notification |

## Submit and approve

Set `DEADLINE` to a future UTC RFC 3339 timestamp and `STEAD_TOKEN` to the owner token. Use a new key for each new submission. An exact retry returns the same delegation, including after its deadline. Reusing the key with a changed request returns `409`.

```sh
BASE=http://127.0.0.1:8080
curl -fsS "$BASE/v1/delegations" \
  -H "Authorization: Bearer $STEAD_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: weekly-design-review-1' \
  --data "{\"goal\":\"Arrange a design review\",\"deadline\":\"$DEADLINE\",\"scheduling\":{\"target\":\"calendar\",\"content\":\"Confirm the review on Friday.\"}}"
```

Copy the returned `delegation.ID` into `ID`. Check status after the worker has prepared the wait. Copy `state.Delegation.Wait.Correlation` into `CORRELATION` and `scope_fingerprint` into `FINGERPRINT`. Set `EXPIRES_AT` to the authorization expiry. Review the exact target and content before recording the approval.

```sh
curl -fsS "$BASE/v1/delegations/$ID" -H "Authorization: Bearer $STEAD_TOKEN"
curl -fsS "$BASE/v1/delegations/$ID/events" \
  -H "Authorization: Bearer $STEAD_TOKEN" -H 'Content-Type: application/json' \
  --data "{\"id\":\"approval-1\",\"kind\":\"approval\",\"text\":\"Send the approved content.\",\"correlation\":\"$CORRELATION\",\"scope_fingerprint\":\"$FINGERPRINT\",\"expires_at\":\"$EXPIRES_AT\"}"
```

Acceptance returns `202`. An identical retry returns `200` with `inserted=false`. A stale scope or modified event with the same ID returns `409`. The worker can progress only after the committed approval passes its own claim checks.

Scheduling can include resource intervals under `scheduling.resources` using `key`, `start` and `end`. To submit a briefing, supply a `briefing` object with `project`, `reviewer`, `target`, `audience` and `sources`. Its optional `format` defaults to `bullets`, and `fallback` defaults to true. The designated reviewer submits `kind=review` with `review.source` and `review.fingerprint`. The owner can use `kind=amend` with `amendment.target`, `amendment.format` or `amendment.take_over`. Other owner events are `input`, `revoke` with `grant_id`, and `evidence` for scheduling.
