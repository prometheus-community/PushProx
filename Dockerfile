# Requires `promu crossbuild` artifacts.
ARG ARCH="amd64"
ARG OS="linux"
FROM quay.io/prometheus/busybox-${OS}-${ARCH}:glibc

ARG ARCH="amd64"
ARG OS="linux"
COPY .build/${OS}-${ARCH}/pushprox-proxy /app/pushprox-proxy
COPY .build/${OS}-${ARCH}/pushprox-client /app/pushprox-client

# The default startup is the proxy.
# This can be overridden with the docker --entrypoint flag or the command
# field in Kubernetes container v1 API.
USER       nobody
ENTRYPOINT ["/app/pushprox-proxy"]
