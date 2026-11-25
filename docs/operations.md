# Deploy Stead

Use Docker with the Compose plugin on a single host. Copy `.env.example` to `.env`, restrict it to your account, and give the app database account, root database account, and API principal independent credentials.

```sh
cp .env.example .env
chmod 600 .env
openssl rand -hex 32
```

Run the last command separately for each credential. Set the database passwords in `STEAD_MYSQL_PASSWORD` and `STEAD_MYSQL_ROOT_PASSWORD`, and put each API token next to its principal in the `STEAD_API_TOKENS` JSON array. The app uses the database account declared by `STEAD_MYSQL_USER`. MySQL listens on the Compose network, and the API binds to `127.0.0.1:8080` on the host by default.

Set `DASHSCOPE_API_KEY` to draft with DashScope and the default `qwen-plus` model. For another OpenAI-compatible provider, specify the full Chat Completions endpoint in `STEAD_MODEL_ENDPOINT`, the model name in `STEAD_MODEL`, and the credential in `STEAD_MODEL_API_KEY`.

```sh
docker compose config --quiet
docker compose up --build -d --wait
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
docker compose ps -a
```

Compose runs the migration job after MySQL passes its readiness query. The API and worker start once migrations succeed. The health endpoint checks that the API process responds. The readiness endpoint also checks the database and migrations. The worker keeps scheduling simulation records in the `deliveries` volume, and the `mysql-data` volume holds the ledger. Keep one worker per database.

To enable Feishu scheduling delivery, supply `STEAD_FEISHU_APP_ID`, `STEAD_FEISHU_APP_SECRET`, and the comma-separated list `STEAD_FEISHU_CHATS`. A scheduling request can then target `feishu:<chat-id>` for a listed chat. Stead still requires approval of the exact destination and content before sending.

Use the trusted CLI inside the application container for maintenance tasks. Set `STEAD_TOKEN` to the principal’s token for HTTP requests.

```sh
curl -fsS http://127.0.0.1:8080/v1/delegations \
  -H "Authorization: Bearer $STEAD_TOKEN"
docker compose exec api stead list -owner alice
docker compose logs --tail 100 migrate api worker
```

Run `docker compose up --build -d --wait` after updating the checkout. Compose keeps both data volumes across container replacement. If startup fails, inspect the migration job logs before restarting the app. Existing database volumes retain their initialized accounts when you change the environment file, so rotate database credentials in MySQL and in the environment together.

## Faults and recovery

Check `/readyz` before routing workload. A lost MySQL session fences the worker from new ledger transactions and causes it to exit. Compose starts a replacement, which reacquires the lock and verifies interrupted actions where receipts exist. If a Feishu request has an ambiguous outcome, check the destination with an operator and record the verified result with `stead settle`. The worker retains the action and resource occupation while the outcome is unknown.

Back up the MySQL ledger before upgrades and verify restores against a separate instance. Back up the local scheduling delivery volume alongside it if you use that provider. A logical dump of the app database can be taken from the Compose database service.

```sh
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --single-transaction --no-tablespaces -u "$MYSQL_USER" "$MYSQL_DATABASE"' > stead-backup.sql
```

Use `docker compose down` to stop the service while keeping both named volumes. [HTTP API](http-api.md)
