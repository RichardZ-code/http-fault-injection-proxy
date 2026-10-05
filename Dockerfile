# No separately downloaded Dockerfile frontend. All runtime targets share /out.
FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
ENV GOWORK=off GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN test "$(go env GOVERSION)" = go1.27.1 && go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY examples ./examples
# P07 supports only linux/amd64; the native race environment remains cgo=1.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o /out/faultproxy ./cmd/faultproxy \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o /out/upstream ./examples/upstream \
 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o /out/retry-client ./examples/retry-client

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS runtime
USER 65532:65532
WORKDIR /
COPY LICENSE /LICENSE

FROM runtime AS upstream
COPY --from=build /out/upstream /upstream
ENTRYPOINT ["/upstream"]

FROM runtime AS retry-client
COPY --from=build /out/retry-client /retry-client
ENTRYPOINT ["/retry-client"]

FROM python:3.14.8-slim-trixie@sha256:c3e521df8b2b498a7a682e7e18676771cb80c6b75b8699af886b2d554ce40151 AS capture
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 65532:65532
WORKDIR /
COPY LICENSE /LICENSE
COPY --from=build /out/faultproxy /faultproxy
COPY scripts/capture_logs.py /capture_logs.py
# The explicit exclusive filename is supplied by Compose, fresh on each launch.
ENTRYPOINT ["python3", "/capture_logs.py"]

# Plain proxy is the default final target; no Python, shell, source or Go cache.
FROM runtime AS proxy
COPY --from=build /out/faultproxy /faultproxy
ENTRYPOINT ["/faultproxy"]
