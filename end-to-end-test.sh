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

if type node_exporter > /dev/null 2>&1 ; then
  node_exporter &
  echo $! > "${tmpdir}/node_exporter.pid"
fi
while ! curl -s -f -L http://localhost:9100; do
  echo 'Waiting for node_exporter'
  sleep 2
done

./pushprox-proxy --log.level=debug &
echo $! > "${tmpdir}/proxy.pid"
while ! curl -s -f -L http://localhost:8080/clients; do
  echo 'Waiting for proxy'
  sleep 2
done

./pushprox-client --log.level=debug --proxy-url=http://localhost:8080 &
echo $! >"${tmpdir}/client.pid"
./pushprox-client --log.level=debug --proxy-url=http://localhost:8080 --fqdn=client2 --label foo=bar --label exporter=node &
echo $! >"${tmpdir}/client2.pid"
while [ "$(curl -s -L 'http://localhost:8080/clients' | jq 'length')" != '2' ]; do
  echo 'Waiting for clients'
  sleep 2
done

echo "Testing client labels..."
clients_response=$(curl -s -L 'http://localhost:8080/clients')

# Check that the first client has empty labels
client1_labels=$(echo "$clients_response" | jq -r '.[] | select(.targets[] != "client2") | .labels')
if [ "$client1_labels" != "{}" ]; then
  echo "ERROR: Expected client1 to have empty labels {}, got $client1_labels"
  exit 1
fi

# Check that client2 has the expected labels
client2_labels=$(echo "$clients_response" | jq -r '.[] | select(.targets[] == "client2") | .labels')
label_value_foo=$(echo "$client2_labels" | jq -r '.foo // "missing"')
label_value_exporter=$(echo "$client2_labels" | jq -r '.exporter // "missing"')
if [ "$label_value_foo" != "bar" ]; then
  echo "ERROR: Expected label foo=bar, got foo=$label_value_foo"
  exit 1
fi
if [ "$label_value_exporter" != "node" ]; then
  echo "ERROR: Expected label exporter=node, got exporter=$label_value_exporter"
  exit 1
fi

echo "✅ Labels test passed: client2 has correct labels (foo=bar, exporter=node), client1 has empty labels"

prometheus --config.file=prometheus.yml --log.level=debug &
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
