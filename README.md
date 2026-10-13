# Description

It is a simple docker container running a postgres server in a aws ec2 instance with a cron job for backups stored in s3, nothing else, that's it.

**Just run a command and you will have your boring database.**

# How to use

## 1. Setup aws cli in your machine

You need to have [aws cli](https://aws.amazon.com/es/cli/) installed in your machine and configured with your aws credentials with enough permissions to create the stack in cloudformation.

## 2. Setup the environment variables

You need to specify the s3 bucket for backups in a `.env` file in the project root folder, the rest of the variables are optional and will use the default values, use `.env.example` as a reference, example:
```bash
BACKUP_CRON_EXPRESSION="0 2 * * *"
S3_BUCKET=just-a-db-backups
AWS_REGION=us-east-1
``` 

## 3. Launch 
Run the following command

```shell
make setup
```

That's all, it will create the following infrastructure in you aws account:

<img src="https://github.com/CrissAlvarezH/just-a-db/blob/main/docs/diagram.png"/>


### Details
- You can see the logs in CloudWatch Logs: `just-a-db/database-logs` (Postgres, pgbouncer, panel) and `just-a-db/backup-runs` (one stream per backup run, nightly or manual, written by `backups/run.sh`). The panel's backups page lists the latest runs, with links to each log in the panel and in CloudWatch.
- Clients connect through **pgbouncer on port 6432**, with TLS required (`sslmode=require`; the certificate is self-signed, in `certs/` on the instance). It pools connections in transaction mode, so clients must disable prepared statements (`prepare: false` in postgres.js). Port 5432 (direct Postgres) only listens on the instance itself.
- Postgres is tuned for a t4g.small (2 GB RAM) and closes sessions idle for 5 minutes; queries slower than 500ms are logged.
- You can see the backups in S3 organized by database name, with files named `backup_<database_name>_<timestamp>.dump` (`pg_dump` custom format), plus roles in `_globals/`. The 14 most recent are kept (`BACKUP_KEEP` in `.backup.env`); old ones are only deleted after every backup of the run succeeded. Set `BACKUP_HEALTHCHECK_URL` (e.g. healthchecks.io) to get alerted when a backup fails or doesn't run.
- Restore a backup: `pg_restore -h <host> -U <user> -d <new_db> --no-owner backup_<db>_<timestamp>.dump`.
- Upgrading an instance created with an older version: see [docs/UPGRADE.md](docs/UPGRADE.md).
- The default database is created with the name `just-a-db`.
- The credentials are created in the deployment stack process, you can see them executing the command `make credentials`, it will store the credentials in the `credentials.txt` file.
- In the root folder you will find `just-a-db.pem` file, that is the private key for the ec2 instance, you need to keep it safe and secure.


### Admin panel

A small web panel (`panel/`, Go + plain HTML, ~5 MB of RAM) behind caddy with
automatic HTTPS:

- Log in with a 6-digit code sent by email (Brevo) to one of `ALLOWED_EMAILS`.
- See databases (size, connections), users, and Postgres' connection usage.
- Create a database with its own user (owner of the database, without access to
  the others); the connection URL is shown once.
- New password for a user, close a database to other users, delete a database.
- Browse and download backups (short-lived S3 links), and run a backup now.

Set it up with `.panel.env` (see `.panel.env.example`) and
`docker-compose up -d --build panel caddy`; details in
[docs/UPGRADE.md](docs/UPGRADE.md#7-admin-panel-optional). Locally, with a `.panel.env`,
`docker-compose -f docker-compose.local.yaml up panel` serves it on
http://localhost:8080 (`DEV_LOG_CODES=true` prints login codes in its logs
instead of emailing them).

### Useful commands

- `make connect` Connect to the ec2 instance using the private key via ssh.
- `make credentials` Get the credentials for the database and store them in the `credentials.txt` file.
- `make destroy` Destroy the infrastructure (aws cloudformation stack).
