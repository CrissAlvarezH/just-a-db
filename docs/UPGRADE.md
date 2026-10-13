# Upgrading a running instance

For an instance created before pgbouncer, TLS and the new backups. Nothing here
replaces the instance or touches the data volume. Step 2 restarts Postgres
(~10 seconds of downtime): do it at a quiet hour.

## 1. Pull the changes

```shell
make connect
cd ~/just-a-db
git pull
sh ./scripts/entrypoint.sh generate-certs
cp -n .panel.env.example .panel.env   # docker-compose needs it to exist (step 7 fills it in)
```

Optional, in `.backup.env`:

```shell
BACKUP_KEEP=14                 # backups kept per database (default 14)
BACKUP_HEALTHCHECK_URL=https://hc-ping.com/<uuid>   # alert when a backup fails or doesn't run
```

## 2. Restart with the new configuration

```shell
docker-compose pull database
docker-compose build backups pgbouncer
docker-compose up -d database pgbouncer
docker-compose ps            # database "healthy", pgbouncer "Up"
```

## 3. Run a backup by hand and check it

```shell
docker-compose run --rm backups
aws s3 ls s3://<bucket>/ --recursive | tail
```

## 4. Open ports 6432, 80 and 443

Either update the stack (`cloudformation.yaml` now has the rules for pgbouncer
and the panel; only the security group changes):

```shell
aws cloudformation update-stack --stack-name just-a-db \
  --template-body file://cloudformation.yaml \
  --parameters ParameterKey=VpcId,UsePreviousValue=true \
    ParameterKey=SubnetId,UsePreviousValue=true \
    ParameterKey=BackupCronExpression,UsePreviousValue=true \
    ParameterKey=ResourcesBucketName,UsePreviousValue=true \
  --capabilities CAPABILITY_NAMED_IAM
```

or add inbound rules for TCP 6432, 80 and 443 to the `just-a-db` security group in the console.

## 5. Move clients to pgbouncer

- Port `6432`, with TLS: `sslmode=require` in the URL, or `ssl: 'require'` in postgres.js.
- Transaction pooling: turn off prepared statements (`prepare: false` in
  postgres.js, `?pgbouncer=true` in Prisma). Don't rely on session state
  (`SET`, advisory locks, `LISTEN`).
- Check it: `psql "host=<ip> port=6432 user=<user> dbname=<db> sslmode=require"`.

## 6. Close direct access (done in the repo: rerun `docker-compose up -d database` after pulling)

Once nothing connects to 5432 from outside:

- Remove the 5432 rule from the security group.
- In `docker-compose.yaml`, change the database port to `"127.0.0.1:5432:5432"`
  (still reachable from the instance itself) and run `docker-compose up -d database`.

## 7. Admin panel (optional)

1. Point a domain to the Elastic IP (an `A` record, e.g. `db.example.com`).
2. In Brevo, verify the sender (or its domain) and create an API key. If the key
   has "authorized IPs" on, add the Elastic IP.
3. Fill in `.panel.env` (see `.panel.env.example`): `PANEL_DOMAIN`,
   `ALLOWED_EMAILS`, `BREVO_API_KEY`, `MAIL_FROM`, `PUBLIC_DB_HOST`.
4. Restart pgbouncer (it now looks up every role in Postgres, so users created
   from the panel can log in through it), then start the panel:

   ```shell
   docker-compose build pgbouncer panel
   docker-compose up -d pgbouncer panel caddy
   docker-compose logs -f caddy   # wait for "certificate obtained successfully"
   ```

5. Open `https://<PANEL_DOMAIN>` and log in with a code sent to your email.

If the backups page says it can't reach S3 with an IMDS error, let containers
reach the instance role:
`aws ec2 modify-instance-metadata-options --instance-id <id> --http-put-response-hop-limit 2`.

---

# Later: replacing the instance

**Changing `ImageId`, `InstanceType` or the subnet in `cloudformation.yaml`
replaces the instance, and the data lives on its root disk: it would be lost.**
The current AMI is Amazon Linux 2, which reached end of support in June 2026, so
plan the move instead of updating the stack in place:

1. Add a separate encrypted gp3 EBS volume for `/var/lib/docker/volumes`, with
   `DeletionPolicy: Snapshot`, plus a DLM policy for daily snapshots.
2. Use Amazon Linux 2023 (AMI from the SSM parameter
   `/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64`) and
   the `docker compose` plugin instead of the standalone binary.
3. Restrict SSH (22) to your IP, or drop it and use SSM Session Manager.
4. Bring the new stack up next to the old one, restore the latest backup
   (`_globals` first, then each database with `pg_restore`), point clients at
   the new IP, and only then destroy the old stack.
