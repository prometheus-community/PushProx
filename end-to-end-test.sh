#!/bin/bash

set -u -o pipefail

tmpdir=$(mktemp -d /tmp/pushprox_e2e_test.XXXXXX)

cleanup() {
  for f in "${tmpdir}"/*.pid ; do
    kill -9 "$(< $f)"
  done
  rm -r "${tmpdir}"
}

trap cleanup EXIT

./node_exporter-0.17.0.linux-amd64/node_exporter &
echo $! > "${tmpdir}/node_exporter.pid"
while ! curl -s -f -L http://localhost:9100; do
  echo 'Waiting for node_exporter'
  sleep 2
done

./build/proxy --log.level=debug &
echo $! > "${tmpdir}/proxy.pid"
while ! curl -s -f -L http://localhost:8080/clients; do
  echo 'Waiting for proxy'
  sleep 2
done

./build/client  --log.level=debug --proxy-url=http://$(hostname):8080 &
echo $! > "${tmpdir}/client.pid"
while [ "$(curl -s -L 'http://localhost:8080/clients' | jq 'length')" != '1' ] ; do
  echo 'Waiting for client'
  sleep 2
done

./prometheus-2.9.1.linux-amd64/prometheus --config.file=prometheus-2.9.1.linux-amd64/prometheus.yml --log.level=debug &
echo $! > "${tmpdir}/prometheus.pid"
while ! curl -s -f -L http://localhost:9090/-/ready; do
  echo 'Waiting for Prometheus'
  sleep 2
done
sleep 15

query='http://localhost:9090/api/v1/query?query=node_exporter_build_info'
while [ $(curl -s -L "${query}" | jq '.data.result | length') != '1' ]; do
  echo 'Waiting for results'
  sleep 2
done
