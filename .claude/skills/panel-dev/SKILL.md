---
name: panel-dev
description: Develop and test the admin panel (panel/, Go + html/template + plain JS/CSS) locally — run the local stack, log in without email, test with curl, gotchas (CSP, sessions, Docker Desktop mounts). Use when changing anything under panel/.
---

# Developing the panel

`panel/` is a Go server (stdlib net/http + html/template, pgx, AWS SDK for S3
and CloudWatch Logs), templates in `panel/templates` (each page is parsed with
`layout.html` + `partials.html`), static JS/CSS in `panel/static`, all
embedded. ~5 MB of RAM.

## Run locally

```shell
docker compose -f docker-compose.local.yaml up --build -d database pgbouncer panel
```

- Panel on http://localhost:8080, Postgres on **5433** (5432 is usually taken
  by another project's container), pgbouncer on 6432.
- Needs `.db.env`, `.backup.env` (S3 bucket + an IAM user's keys; locally
  `just-a-db-backups-local`) and `.panel.env` (`ALLOWED_EMAILS`, Brevo, or
  `DEV_LOG_CODES=true` to print login codes in `docker compose logs panel`).
- Rebuild after every change: `docker compose -f docker-compose.local.yaml up --build -d panel`.
  A plain `up` keeps the old image (check with `curl -sI` for a header you changed).
- If the repo lives under `/private/tmp`, Docker Desktop mounts `certs/` empty
  and pgbouncer runs without TLS: keep the repo under `~/`.
- CloudWatch ("Últimas corridas") doesn't work locally: the section shows an
  error, which is expected.

## Testing with curl (no email)

Sessions are signed cookies: `base64url(email|expiryUnix|csrf) + "." +
base64url(HMAC-SHA256(key, payload))`, key = `SHA256("just-a-db-panel/session/" +
POSTGRES_PASSWORD)` (or `SESSION_SECRET`). Mint one for local tests:

```shell
python3 -c "
import hmac,hashlib,base64,time,sys
key=hashlib.sha256(('just-a-db-panel/session/'+sys.argv[2]).encode()).digest()
p=f'{sys.argv[1]}|{int(time.time())+600}|t'
b=lambda x: base64.urlsafe_b64encode(x).rstrip(b'=').decode()
print(b(p.encode())+'.'+b(hmac.new(key,p.encode(),hashlib.sha256).digest()))" <allowed email> <POSTGRES_PASSWORD>
# curl -b "jad_session=<token>" -d csrf=t … (the csrf is the payload's third field)
```

Only for the local stack. Don't mint cookies for production.

## Gotchas

- CSP is `default-src 'self'`: no inline `<script>` or `style=""` attributes.
  Set dynamic styles from JS (see `data-w` bars in `static/app.js`).
- `Referrer-Policy` must stay `same-origin`: with `no-referrer` browsers send
  `Origin: null` on form posts and `sameOrigin` rejects them.
- POSTs need the session's CSRF token in a `csrf` field.
- Destructive rules live in the handlers, not only the UI: dropping a database
  requires a backup of it from the last 10 minutes (`deleteBackupMaxAge`).
- `go vet ./... && go test ./...` in `panel/` before committing.

Ship it with the `ship-panel-image` skill.
