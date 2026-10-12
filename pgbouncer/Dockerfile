FROM alpine:3.21

RUN apk add --no-cache pgbouncer su-exec \
  && addgroup -S bouncer && adduser -S -G bouncer bouncer

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 6432

ENTRYPOINT ["/entrypoint.sh"]
