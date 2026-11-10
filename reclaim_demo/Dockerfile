ARG OTELCOL_VERSION=0.164.0

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/reclaim_demo .

FROM otel/opentelemetry-collector-contrib:${OTELCOL_VERSION} AS otelcol

FROM debian:bookworm-slim
WORKDIR /demo
COPY --from=build /out/reclaim_demo /usr/local/bin/reclaim_demo
COPY --from=otelcol /otelcol-contrib /usr/local/bin/otelcol-contrib
COPY demo.yaml collector.yaml ./
ENTRYPOINT ["/usr/local/bin/reclaim_demo"]
CMD ["--config", "demo.yaml"]
