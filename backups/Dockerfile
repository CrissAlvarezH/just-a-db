FROM postgres:17.2-alpine3.21

RUN apk add --no-cache aws-cli

WORKDIR /app

COPY run.sh /app/run.sh
RUN chmod +x /app/run.sh

CMD ["/app/run.sh"]
