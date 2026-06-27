FROM golang:1.23-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/nexusproxy ./cmd/nexusproxy

FROM scratch

COPY --from=build /out/nexusproxy /nexusproxy
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY config.example.json ./

ENV NEXUS_HOST=0.0.0.0
EXPOSE 8787

ENTRYPOINT ["/nexusproxy"]
CMD ["--config", "config.example.json"]
