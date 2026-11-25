# Personal Agent

Stead runs owner-scoped delegations and source tracking with a MySQL ledger. Run the [local setup](../README.md#run-locally) first. The command examples use the trusted local CLI. The briefing provider and scheduling delivery spool are reference environments for exercising handoffs and side effects. The Feishu boundary has local adapter tests.

## Use the browser workspace

Start the API, worker and database using the [deployment commands](operations.md), then open [Stead on loopback](http://127.0.0.1:8080/). Connect with your principal's API token. The browser keeps it for the current page session and clears it on disconnect.

- **New delegation** creates a scheduling or briefing task. Optional model drafting fills supported fields to review before submission.
- **Delegations** shows wait conditions, deadlines, exact scope, recent events and receipts. Approve, revoke, amend or report completion from the selected delegation. Status refresh pauses while an event is being edited.
- **Reviews** opens a specific briefing by ID for its designated reviewer. It shows current source fingerprints and shareable excerpts. A review applies to the matching fingerprint.
- **Notifications** shows unread source updates for a project and allows individual acknowledgment. Source intake and policy release remain available through their existing collector and CLI commands.

## Draft a delegation

Structured commands work offline. For natural-language drafts, set `STEAD_MODEL_ENDPOINT` to a full Chat Completions endpoint, set `STEAD_MODEL` to your model name, and set `STEAD_MODEL_API_KEY` if the endpoint requires a key. `draft -text` sends the supplied description to that endpoint. It returns a draft for review, with scheduling, briefing, or tracking fields.

```sh
./stead draft -text 'Schedule a review and confirm it with the design team' > draft.json
./stead draft -file draft.json
./stead submit -draft draft.json -owner alice -ttl 1h
```

Submission records the delegation. The worker, approval events, and provider receipts advance it. The model does not receive ledger access or execution tools. A briefing draft can also be passed to `briefing submit -draft`. The owner, project, reviewer, destination, deadline and required audience are supplied separately. Tracking drafts describe the source and object to configure in `track` or an ingress collector.

## Briefing delegation

The reference provider stores each source and audience under an owner and project. Mark values that may appear in the briefing as shareable. Values kept for review contribute a fingerprint to the delivery guard. The resulting draft and proposal trace omit their raw contents.

```sh
./stead briefing fact -owner alice -project launch -source summary \
  -value 'Launch is scheduled for Thursday.' -shareable
./stead briefing fact -owner alice -project launch -source budget \
  -value 'Internal estimate, for review only.'
./stead briefing audience -owner alice -project launch -target design -class internal
ID=$(./stead briefing submit -owner alice -project launch -reviewer finance \
  -target design -sources summary,budget -goal 'Send the reviewed launch briefing' -ttl 10m)
./stead status -id "$ID"
```

Review binds a named reviewer to the fingerprint shown by `briefing fact` or under `Delegation.Briefing.Items` after the worker has read the sources. Replace the two fingerprint values below.

```sh
./stead briefing review -id "$ID" -event-id "$ID-summary-review" -sender finance \
  -source summary -fingerprint "$SUMMARY_FINGERPRINT" -text 'Confirmed the launch date.'
./stead briefing review -id "$ID" -event-id "$ID-budget-review" -sender finance \
  -source budget -fingerprint "$BUDGET_FINGERPRINT" -text 'Confirmed the internal estimate.'
./stead status -id "$ID"
```

After the worker presents the prepared artifact and approval wait, use its `Wait.Correlation` to approve the exact document and destination. The provider accepts an action only while the source and audience versions match. It records the outcome independently of the harness action. Once the provider has proof, the worker can complete the delegation.

```sh
read -r CORRELATION
./stead event -id "$ID" -event-id "$ID-send" -sender alice -kind approval \
  -correlation "$CORRELATION" -text 'I approve sending this version to Design.'
./stead briefing deliveries -id "$ID"
./stead list -owner alice
```

Update a fact or audience with `-expect` set to its previously read revision. Use `briefing amend` to change the format, change the destination or take over one source item. Source changes refresh the affected item. Recipient or format changes can reuse the already prepared items and their reviews. Approval of an earlier artifact does not transfer to a changed artifact or audience.

By default, missing review or approval at the deadline returns the available draft through the delegation result. `-fallback=false -ask-lead 15m` schedules a request for a decision ahead of the deadline. `briefing manual-delivery` records an independent owner delivery in the reference provider. Both normal and manual delivery have inspectable proof. The worker recovers interrupted actions, reads existing provider receipts when available, and leaves uncertain outcomes awaiting verification.

## Tracking and personal preferences

The source assigns stable object IDs and event IDs. A higher revision may change the title without creating a second tracked item. The feed holds pending immediate and digest entries together with the exact policy version or preference that determined the result. A source observation can name an affected conclusion.

```sh
./stead track observe -owner alice -project reading -source papers -object paper-42 \
  -event-id paper-42-v1 -revision 1 -title 'First paper version'
./stead track observe -owner alice -project reading -source papers -object paper-42 \
  -event-id paper-42-v2 -revision 2 -title 'Revised paper title' \
  -material -conclusion launch-baseline
./stead track feed -owner alice -project reading
./stead track history -owner alice -project reading -source papers -object paper-42
```

Use `track ack -seq` for a feed entry. `track mode -mode muted` keeps subsequent versions current while suppressing notifications. Resume with `active`. `stopped` and `taken_over` close tracking for that object and ignore subsequent results.

```sh
./stead preference set -owner alice -project reading -scope project \
  -value silent -text 'Keep the reading feed quiet for an hour.' -ttl 1h
./stead preference resolve -owner alice -project reading
```

Task preferences take precedence over project and owner settings. Once a temporary override expires or is revoked, later observations use the preference then in force. The preference record preserves its original wording and source.

For unattended ingestion, give a trusted collector the same bearer token and leave the local adapter running. The adapter binds all delivered observations to the owner, project and source supplied at startup.

```sh
export STEAD_INGRESS_TOKEN=$(openssl rand -hex 32)
./stead ingress -listen 127.0.0.1:8787 -owner alice -project reading -source papers
```

Set the same token in the collector environment. Submit to the loopback endpoint with the event and object IDs the source assigned. Retrying the same event preserves its sequence number.

```sh
curl -fsS http://127.0.0.1:8787/observations \
  -H "Authorization: Bearer $STEAD_INGRESS_TOKEN" -H 'Content-Type: application/json' \
  -d '{"object_id":"paper-42","event_id":"paper-42-v3","revision":3,"title":"Version three","conclusion_ids":["launch-baseline"]}'
```

## Recursive improvement

A notification policy chooses immediate alert or digest. The improver proposes and ranks edits to that policy. It also proposes typed edits to its own search and evaluation method. Each selected method is recorded with a parent version and actually loaded in the next round. User preference and tracking mode are applied independently. Ownership, authority, and the delivery kernel sit outside the improver.

Run the offline study without a database or model.

```sh
./stead rsi study -seed 27 -rounds 3 -budget 10 > rsi-study.json
```

Compare `runs[].holdout` across the frozen improver, simple search, and recursive improver arms. In a recursive round, `loaded.improver`, `active_after.improver`, and `parent_comparison` show which method generated the candidates and whether replacing it with its parent changed the chosen successor. Each arm reports its candidate and case-evaluation counts. Parent replays are reported separately.

To study a real tracking project, initialize it with `rsi init -owner alice -project reading`. Label reviewed `track history` entries with `rsi feedback -seq SEQUENCE -expected immediate|digest`. Use `-pair` to link contrasting outcomes and `-anchor` to add a reference case without making it a repair target. `rsi export-feedback` exports decision features and labels. `rsi improve -dataset PATH -include-feedback` uses the project feedback in its development partition. `rsi example -part selection` shows the dataset shape.

Promoting a candidate within an experiment changes its research lineage. To put a version into the live tracking path, first pick the candidate and run `rsi activate -policy VERSION -holdout PATH` with independent cases isolated by family. The live gate checks critical alerts and holds non-regression against the current release. Tracking records the loaded live ID in `PolicyVersion`. `rsi rollback -live -policy baseline` returns the project to its initial live rules. Leave off `-live` to restore a saved research policy or improver.

The built-in study is synthetic and reproducible. Holdout and mechanism results describe these cases. User time saved, correction frequency under live usage, and behavior with real messaging accounts require separate measurement.
