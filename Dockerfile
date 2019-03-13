FROM alpine:latest as certificates
RUN apk update && apk add --no-cache ca-certificates && update-ca-certificates

FROM golang:1.12 as builder

RUN go get github.com/robustperception/pushprox/proxy
WORKDIR $GOPATH/src/github.com/robustperception/pushprox/proxy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build

RUN go get github.com/robustperception/pushprox/client
WORKDIR $GOPATH/src/github.com/robustperception/pushprox/client
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build


FROM scratch

# Copy certs from alpine as they don't exist from scratch
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy client and proxy from builder
COPY --from=builder /go/src/github.com/robustperception/pushprox/proxy /app
COPY --from=builder /go/src/github.com/robustperception/pushprox/client /app

# default startup is the proxy. 
# Can be overridden with the docker --entrypoint flag, or the command field in Kubernetes container v1 API
ENTRYPOINT ["/app/proxy"]
