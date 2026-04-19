#!/usr/bin/env bash

set -u -o pipefail

node_exporter_host=${NODE_EXPORTER_HOST:-localhost}
prometheus_host=${PROMETHEUS_HOST:-localhost}

tmpdir=$(mktemp -d /tmp/pushprox_e2e_test.XXXXXX)

cleanup() {
  for f in "${tmpdir}"/*.pid ; do
    kill -9 "$(< $f)"
  done
  rm -r "${tmpdir}"
}

trap cleanup EXIT

if type node_exporter > /dev/null 2>&1 ; then
  node_exporter &
  echo $! > "${tmpdir}/node_exporter.pid"
fi
while ! curl -s -f -L "http://${node_exporter_host}:9100" > /dev/null; do
  echo "Waiting for node_exporter host=${node_exporter_host}"
  sleep 2
done

if [[ ! -f 'pushprox-proxy' ]] ; then
  echo 'ERROR: Missing pushprox-proxy binary'
  exit 1
fi
./pushprox-proxy --log.level=debug &
echo $! > "${tmpdir}/proxy.pid"
while ! curl -s -f -L http://localhost:8080/clients > /dev/null; do
  echo 'Waiting for proxy'
  sleep 2
done

if [[ ! -f 'pushprox-client' ]] ; then
  echo 'ERROR: Missing pushprox-client binary'
  exit 1
fi
./pushprox-client  --log.level=debug --proxy-url=http://localhost:8080 &
echo $! > "${tmpdir}/client.pid"
while [ "$(curl -s -L 'http://localhost:8080/clients' | jq 'length')" != '1' ] ; do
  echo 'Waiting for client'
  sleep 2
done

if type prometheus > /dev/null 2>&1 ; then
  prometheus --config.file=end-to-end/prometheus.yml --log.level=debug &
  echo $! > "${tmpdir}/prometheus.pid"
fi
while ! curl -s -f -L "http://${prometheus_host}:9090/-/ready"; do
  echo "Waiting for Prometheus host=${prometheus_host}"
  sleep 2
done
sleep 15

query="http://${prometheus_host}:9090/api/v1/query?query=node_exporter_build_info"
while [ $(curl -s -L "${query}" | jq '.data.result | length') != '1' ]; do
  echo 'Waiting for results'
  sleep 2
done
