FROM golang:latest as builder

RUN go get github.com/robustperception/pushprox/proxy
WORKDIR $GOPATH/src/github.com/robustperception/pushprox/proxy
RUN go build


FROM scratch

COPY --from=builder /go/src/github.com/robustperception/pushprox/proxy /
WORKDIR /
CMD ["./proxy"]


