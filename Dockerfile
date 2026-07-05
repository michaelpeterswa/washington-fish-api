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

# CMD (not ENTRYPOINT) so a supplied command REPLACES the default instead of
# appending to it. Default run is the API server; overriders run a worker:
#   - Fly release_command: "/wfa-worker migrate"
#   - Fly cron-manager machines: "/wfa-worker <job>"
#   - k8s CronJob: command: ["/wfa-worker", "<job>"]
# With an ENTRYPOINT, Fly would exec `/wfa-api /wfa-worker migrate` (args are
# appended), which just starts the server and hangs the release step.
CMD ["/wfa-api"]
