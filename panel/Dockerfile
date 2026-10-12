# Built from the repo root (it needs backups/run.sh): see docker-compose.yaml.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY panel/go.mod panel/go.sum ./
RUN go mod download
COPY panel/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /panel .

# Same image as the backups container, so "Hacer backup ahora" runs the same
# run.sh with the same pg_dump.
FROM postgres:17-alpine
RUN apk add --no-cache aws-cli bash
COPY backups/run.sh /app/run.sh
COPY --from=build /panel /app/panel
RUN chmod +x /app/run.sh
USER postgres
EXPOSE 8080
CMD ["/app/panel"]
