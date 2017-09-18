# PushProx

PushProx is a client and proxy that allows transversing of NAT and other
similar network topologies by Prometheus, while still following the pull model.

While this is reasonably robust in practice, this is a work in progress.

## Running

First build the proxy and client:

```
go get github.com/robustperception/pushprox/{client,proxy}
cd ${GOPATH-$HOME/go}/src/github.com/robustperception/pushprox/client
go build
cd ${GOPATH-$HOME/go}/src/github.com/robustperception/pushprox/proxy
go build
```

Run the proxy somewhere both Prometheus and the clients can get to:

```
./proxy
```

On every target machine run the client, pointing it at the proxy:
```
./client --proxy-url=http://proxy:8080/
```

In Prometheus, use the proxy as a `proxy_url`:

```
scrape_configs:
- job_name: node
  proxy_url: http://proxy:8080/
  static_configs:
    - targets: ['client:9100']  # Presuming the FQDN of the client is "client".
```

If the target must be scraped over SSL/TLS, add:
```
  params:
    _scheme: [https]
```
rather than the usual `scheme: https`. Only the default `scheme: http` works with the proxy,
so this workaround is required.

## Service Discovery

The `/clients` endpoint will return a list of all registered clients in the format
used by `file_sd_configs`. You could use wget in a cronjob to put it somewhere
file\_sd\_configs can read and then then relabel as needed.

## How It Works

The client registers with the proxy, and awaits instructions.

When Prometheus performs a scrape via the proxy, the proxy finds
the relevant client and tells it what to scrape. The client performs the scrape,
sends it back to the proxy which passes it back to Prometheus.

## Security

There is no authentication or authorisation included, a reverse proxy can be
put in front though to add these.

Running the client allows those with access to the proxy or the client to access
all network services on the machine hosting the client.
