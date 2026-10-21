# Build stage
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/logflux ./cmd/collector \
 && mkdir -p /out/data

# Runtime stage
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/logflux /logflux
# Data directory owned by the nonroot user (uid 65532)
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
# 9090: API + web UI, 8080: HTTP ingest, 5140: syslog
EXPOSE 9090 8080 5140/udp 5140/tcp
ENTRYPOINT ["/logflux", "-data", "/data"]
CMD ["http", ":8080"]
