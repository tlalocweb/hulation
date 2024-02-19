#!/bin/bash
mkdir -p ./test_ch_data
mkdir -p ./test_ch_logs
docker run -d \
    -p 9010:9000 -p 8133:8123 \
    --cap-add=SYS_NICE --cap-add=NET_ADMIN --cap-add=IPC_LOCK \
    -v $(realpath ./test_ch_data):/var/lib/clickhouse/ \
    -v $(realpath ./test_ch_logs):/var/log/clickhouse-server/ \
    --name hulation-test-clickhouse --ulimit nofile=262144:262144 clickhouse/clickhouse-server
