# Same major version as the database: pg_dump refuses to dump a newer server.
FROM postgres:17-alpine

RUN apk add --no-cache aws-cli bash jq

WORKDIR /app

COPY run.sh /app/run.sh
RUN chmod +x /app/run.sh

CMD ["/app/run.sh"]
