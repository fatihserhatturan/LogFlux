# Build stage
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /logflux ./cmd/collector

# Runtime stage
FROM gcr.io/distroless/static:nonroot
COPY --from=build /logflux /logflux
EXPOSE 8080 5140/udp 5140/tcp
ENTRYPOINT ["/logflux"]
CMD ["http", ":8080"]
