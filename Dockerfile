# -=-=-=-=-=-=- Compile Image -=-=-=-=-=-=-

FROM golang:1 AS stage-compile

WORKDIR /go/src/app
COPY . .

# hadolint ignore=DL3062
RUN go get -d -v ./... \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/wfa-api ./cmd/wfa-api \
    && CGO_ENABLED=0 GOOS=linux go build -o /out/wfa-worker ./cmd/wfa-worker

# -=-=-=-=- Final Distroless Image -=-=-=-=-

# hadolint ignore=DL3007
FROM gcr.io/distroless/static-debian12:latest AS stage-final

COPY --from=stage-compile /out/wfa-api /wfa-api
COPY --from=stage-compile /out/wfa-worker /wfa-worker

# Default to the API server; the worker CronJob overrides command to
# ["/wfa-worker", "<job>"].
ENTRYPOINT ["/wfa-api"]
