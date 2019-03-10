FROM golang:1.12 as builder

RUN go get github.com/robustperception/pushprox/proxy
WORKDIR $GOPATH/src/github.com/robustperception/pushprox/proxy
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build


FROM scratch

COPY --from=builder /go/src/github.com/robustperception/pushprox/proxy /app

ENTRYPOINT ["/app/proxy"]
