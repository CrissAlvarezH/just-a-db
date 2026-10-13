---
name: ship-panel-image
description: Build the panel's Docker image on the laptop (arm64) and load it on the server over SSH, so the server never compiles Go. Use whenever panel/ changes need to reach production.
---

# Shipping the panel image

The server (t4g.small, 1.9 GB RAM, no swap) runs Postgres; compiling Go there
can get the OOM killer to pick Postgres. The laptop (Apple Silicon) and the
server are both arm64, so build here and stream the image over SSH.

```shell
# From the repo root, on the commit that is (or will be) on main:
docker build --platform linux/arm64 -f panel/Dockerfile -t just-a-db-panel:server .

docker save just-a-db-panel:server | gzip | ~/Documents/personal/just-a-db/connect.sh '
  gunzip | docker load >/dev/null &&
  docker tag just-a-db-panel:server just-a-db-panel:latest &&
  docker rmi just-a-db-panel:server >/dev/null &&
  cd ~/just-a-db && git pull -q --ff-only origin main &&
  docker-compose up -d --no-build --no-deps panel &&
  docker image prune -f >/dev/null'
```

- ~155 MB compressed, about a minute.
- `--no-build`: compose uses the loaded `just-a-db-panel` image (the compose
  file names it) instead of building. `--no-deps`: doesn't touch `database`.
- The image is built FROM `postgres:17-alpine` with aws-cli and
  `backups/run.sh`, because "Hacer backup ahora" and restores run `pg_dump`,
  `pg_restore` and `psql` inside the panel. The Dockerfile's build context is
  the repo root for that reason.
- Sessions are signed cookies (key derived from the Postgres password), so
  restarting the panel doesn't log anyone out.

Verify: `curl -s -o /dev/null -w "%{http_code}" https://db.sanninlabs.org/login`
→ 200, and `docker logs just-a-db-panel-1` shows `panel listening on :8080`.
