# just-a-db

Postgres 17 in Docker on one EC2 instance (CloudFormation), with pgbouncer
(6432, TLS), nightly backups to S3, and an admin panel (`panel/`, Go) behind
caddy at https://db.sanninlabs.org. It is the **production database of several
apps** (Moldure, wompi-router, content-studio): a restart of `database` or
`pgbouncer` is downtime for all of them.

## Rules

- Don't connect to the production databases (psql, `docker exec … psql`, the
  apps' credentials) unless the user allows it for that specific task. Use
  logs, `docker ps`, the panel and CloudWatch to check things.
- Before restarting Postgres or changing the stack: take a backup, and for
  CloudFormation review a change set (an instance replacement loses the data).
- Commits: author `Cristian Alvarez H <alvarezhernandezcristian1@gmail.com>`;
  push with `git push git@github.com-personal:CrissAlvarezH/just-a-db.git <branch>`
  (the default GitHub account on the laptop is a work one).
- The server deploys `main`. Merge before deploying; don't deploy branches.

## Skills (`.claude/skills/`)

- `deploy-server`: applying changes on the server, what restarts what, checks.
- `ship-panel-image`: build the panel image on the laptop and load it over SSH.
- `aws-infra`: change sets, EBS resize, Route 53, the backups bucket.
- `connect-app-to-pgbouncer`: moving an app to pgbouncer / its own database,
  per driver, Vercel env vars and preview testing.
- `panel-dev`: running and testing the panel locally.
- `backups`: how run.sh works, running, logs, restoring.

Also: `docs/UPGRADE.md` (upgrading an old instance; replacing the instance later).
