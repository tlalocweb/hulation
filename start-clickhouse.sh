#!/bin/bash
docker run -d \
    -p 9000:9000 -p 8123:8123 \
    --cap-add=SYS_NICE --cap-add=NET_ADMIN --cap-add=IPC_LOCK \
    -v $(realpath ./ch_data):/var/lib/clickhouse/ \
    -v $(realpath ./ch_logs):/var/log/clickhouse-server/ \
    --name hulation-clickhouse --ulimit nofile=262144:262144 clickhouse/clickhouse-server
