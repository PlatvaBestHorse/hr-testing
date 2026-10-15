FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=cloud_ca if [ -f /run/secrets/cloud_ca ]; then export SSL_CERT_FILE=/run/secrets/cloud_ca; fi; go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -o /operator/bootstrap ./cmd/bootstrap && \
    CGO_ENABLED=0 go build -trimpath -o /operator/set-password ./cmd/set-password && \
    CGO_ENABLED=0 go build -trimpath -o /operator/org-resume ./cmd/org-resume && \
    CGO_ENABLED=0 go build -trimpath -o /backup/backup ./cmd/backup

# Use the same major PostgreSQL client as the private production database.
# No Go toolchain or Docker socket is needed by backup/restore operations.
FROM postgres:17-bookworm AS backup
COPY --from=build /backup/backup /app/backup
ENTRYPOINT ["/app/backup"]

FROM alpine:3.23 AS base
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001

FROM base AS operator
COPY --from=build /operator/ /app/
CMD ["/app/bootstrap"]

FROM base AS runtime
COPY --from=build /out/ /app/
EXPOSE 8080
CMD ["/app/api"]
