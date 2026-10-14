FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=cloud_ca if [ -f /run/secrets/cloud_ca ]; then export SSL_CERT_FILE=/run/secrets/cloud_ca; fi; go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate

FROM alpine:3.23
WORKDIR /app
COPY --from=build /out/ /app/
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
EXPOSE 8080
CMD ["/app/api"]
