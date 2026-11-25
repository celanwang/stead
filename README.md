# Stead

**I’ll go instead.**

Stead is the volunteer tucked inside *instead*. Hand it a task, and that part of your day is yours again.

[Project docs](https://app.notion.com/p/sanduo/Stead-ed7c56617fb883acb3c2016d8f9d07ff) — Stead’s core idea and product design.

Stead runs long-lived scheduling and briefing delegations, tracks source changes, and learns notification policies from reviewed feedback. [Personal Agent guide](docs/personal-agent.md)

## Run locally

Use Go 1.27.0 or later and a MySQL 8 instance. The commands below start a dedicated MySQL 8.0.40 container on loopback. Run the readiness query again if the server is still starting.

```sh
docker run --rm -d --name stead-mysql -p 127.0.0.1:3307:3306 \
  -e MYSQL_ROOT_PASSWORD=stead-root-local -e MYSQL_DATABASE=stead \
  -e MYSQL_USER=stead -e MYSQL_PASSWORD=stead-local mysql:8.0.40
docker exec stead-mysql mysql -h127.0.0.1 -ustead -pstead-local stead \
  -e 'SELECT VERSION(), @@default_storage_engine'
```

Build the CLI, apply the versioned InnoDB migrations, and leave the worker running in its own terminal.

```sh
export STEAD_MYSQL_DSN='stead:stead-local@tcp(127.0.0.1:3307)/stead'
go build -o ./stead ./cmd/stead
./stead --help
./stead migrate
DELIVERIES=$(mktemp -d)
./stead worker -deliveries "$DELIVERIES"
```

The local CLI is a trusted input for the simulator. In a second terminal, set the same DSN and submit a scheduling request. The submission command exits after writing the delegation. Check status until the worker sets `Delegation.Wait` to an approval wait.

```sh
export STEAD_MYSQL_DSN='stead:stead-local@tcp(127.0.0.1:3307)/stead'
ID=$(./stead submit -owner alice -goal 'Schedule a review' \
  -target calendar -content 'Confirm the review' -ttl 10m)
./stead status -id "$ID"
```

Paste the current `Delegation.Wait.Correlation` value at the `read` prompt. The worker queues and simulates the approved action. Reuse the same event ID if you retry an input.

```sh
read -r CORRELATION
./stead event -id "$ID" -event-id "$ID-approval" -sender alice -kind approval \
  -correlation "$CORRELATION" -text 'I approve confirming the review.' -valid-for 5m
./stead status -id "$ID"
```

Once the action is `delivered`, its receipt references an independent delivery record in the worker’s directory. Report completion evidence to close the delegation, then check the recorded result.

```sh
./stead event -id "$ID" -event-id "$ID-proof" -sender alice -kind evidence \
  -text 'Calendar confirmation 17'
./stead status -id "$ID"
```

Run the same worker command with the same directory after a restart. MySQL keeps the original wait and deadline. The worker marks interrupted sends `unknown` and retains their resource occupations. Use `stead settle -h` to record a verified scheduling outcome. Briefing deliveries use a reference provider with separately recorded receipts. Feishu ingress and receipt conversion have local tests.

When status shows a result, export and replay the audit records with the simulation package. Replay uses the stored proposals, decision times, and receipts. The file command runs without a database connection.

```sh
RECORDS=$(mktemp)
./stead replay -id "$ID" -export > "$RECORDS"
env -u STEAD_MYSQL_DSN ./stead replay -file "$RECORDS"
```

## Verify

```sh
gofmt -l cmd internal tests
go build ./...
go test ./...
```

Use a dedicated database for the MySQL acceptance suite. The following commands provision one in the local container and run the suite against it. The suite also applies and rechecks the migrations.

```sh
docker exec -i stead-mysql mysql -uroot -pstead-root-local <<'SQL'
CREATE DATABASE IF NOT EXISTS stead_acceptance;
GRANT ALL ON stead_acceptance.* TO 'stead'@'%';
SQL
STEAD_TEST_MYSQL_DSN='stead:stead-local@tcp(127.0.0.1:3307)/stead_acceptance' \
  go test -count=1 -v ./...
```

The acceptance suite covers restart and deduplication, revocation, overlapping claims, interrupted sends, deadline fallback and replay. It also checks briefing document and audience versions, source tracking with preferences, and the feedback-to-RSI release path. Without `STEAD_TEST_MYSQL_DSN`, Go skips the MySQL tests.

## Project layout

- `cmd/stead/` contains the local CLI and background worker.
- `internal/delegation/` contains the domain model, plugin contracts, and runtime.
- `internal/ledger/` contains GORM transactions and versioned MySQL InnoDB migrations.
- `internal/scenarios/scheduling/` contains the scheduling proposer.
- `internal/scenarios/briefing/` binds review, draft preparation, and verified delivery.
- `internal/scenarios/tracking/` reduces source observations into versioned objects and notifications.
- `internal/preferences/` resolves scoped user preferences and overrides.
- `internal/intent/` validates task drafts and adapts an optional model endpoint.
- `internal/rsi/` runs bounded improvement and evaluates releases for tracking.
- `internal/channels/ingress/` accepts authenticated source updates on loopback.
- `internal/channels/feishu/` translates verified messages and delivery reports at the channel boundary.
- `internal/simulation/` contains the controllable clock, simulated delivery records, fault hook, and offline replay.
- `tests/acceptance/` exercises the runtime against MySQL. [Kernel design](https://app.notion.com/p/421c56617fb881768a5bf017788881bd)
