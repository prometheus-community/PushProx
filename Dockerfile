FROM golang:latest as build
RUN apt-get update && apt-get install -y make curl
WORKDIR /app
COPY . .
RUN make build

FROM quay.io/prometheus/busybox-amd64-linux:glibc

COPY --from=build /app/pushprox-proxy /app/pushprox-client /

# The default startup is the proxy.
# This can be overridden with the docker --entrypoint flag or the command
# field in Kubernetes container v1 API.
USER       nobody
ENTRYPOINT ["/pushprox-proxy"]
