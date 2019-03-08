FROM golang:latest as builder

RUN go get github.com/robustperception/pushprox/proxy
WORKDIR $GOPATH/src/github.com/robustperception/pushprox/proxy
RUN go build

FROM golang:alpine
COPY --from=builder /go/src/github.com/robustperception/pushprox/proxy /app/
WORKDIR /app

CMD ["./proxy"]


