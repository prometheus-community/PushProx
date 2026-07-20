#!/usr/bin/env bash

set -u -o pipefail

tmpdir=$(mktemp -d /tmp/pushprox_e2e_test.XXXXXX)

cleanup() {
  for f in "${tmpdir}"/*.pid ; do
    kill -9 "$(< $f)"
  done
  rm -r "${tmpdir}"
}

trap cleanup EXIT

get_clients() {
  curl -s -f -L http://localhost:8080/clients
}

run_client() {
  ./pushprox-client --log.level=debug --fqdn="${HOSTNAME}" --proxy-url=http://localhost:8080 2>&1 | sed 's/^/pushprox-client: /'
}

run_proxy() {
  ./pushprox-proxy --log.level=debug 2>&1 | sed 's/^/pushprox-proxy: /'
}

run_node_exporter() {
  node_exporter 2>&1 | sed 's/^/node_exporter: /'
}

run_prometheus() {
  prometheus --config.file=end-to-end/prometheus.yml --log.level=debug 2>&1 | sed 's/^/prometheus: /'
}

if type node_exporter > /dev/null 2>&1 ; then
  echo "INFO: Starting node_exporter"
  run_node_exporter &
  pidof node_exporter > "${tmpdir}/node_exporter.pid"
fi
while ! curl -s -f -L "http://localhost:9100" > /dev/null; do
  echo 'INFO: Waiting for node_exporter'
  sleep 2
done

if [[ ! -f 'pushprox-proxy' ]] ; then
  echo 'ERROR: Missing pushprox-proxy binary'
  exit 1
fi
run_proxy &
pidof pushprox-proxy > "${tmpdir}/proxy.pid"
while ! get_clients > /dev/null; do
  echo 'INFO: Waiting for proxy'
  sleep 2
done

if [[ ! -f 'pushprox-client' ]] ; then
  echo 'ERROR: Missing pushprox-client binary'
  exit 1
fi
run_client &
pidof pushproxy-proxy > "${tmpdir}/client.pid"
while [ "$(get_clients | jq 'length')" != '1' ] ; do
  echo 'INFO: Waiting for client'
  sleep 2
done

echo "INFO: Found clients: $(get_clients | jq -rc .)"

if type prometheus > /dev/null 2>&1 ; then
  run_prometheus &
  pidof prometheus > "${tmpdir}/prometheus.pid"
fi
while ! curl -s -f -L "http://localhost:9090/-/ready"; do
  echo 'INFO: Waiting for Prometheus'
  sleep 2
done
sleep 15

query="http://localhost:9090/api/v1/query?query=node_exporter_build_info"
while [ $(curl -s -L "${query}" | jq '.data.result | length') != '1' ]; do
  echo 'INFO: Waiting for results'
  sleep 2
done
