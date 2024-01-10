#!/bin/bash
docker run -d \
    --network=host \
    --cap-add=SYS_NICE --cap-add=NET_ADMIN --cap-add=IPC_LOCK \
    -v $(realpath ./ch_data):/var/lib/clickhouse/ \
    -v $(realpath ./ch_logs):/var/log/clickhouse-server/ \
    --name hulation-clickhouse --ulimit nofile=262144:262144 clickhouse/clickhouse-server
