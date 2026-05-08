#!/bin/bash
mkdir -p ./test_ch_data
mkdir -p ./test_ch_logs
# Mount a users.d override so the default user accepts connections from
# the docker bridge (host → container hop). Recent CH images ship a
# default-user.xml that limits default to ::1/127.0.0.1, which fails
# auth for any TCP connection from outside the container.
docker run -d \
    -p 9010:9000 -p 8133:8123 \
    --cap-add=SYS_NICE --cap-add=NET_ADMIN --cap-add=IPC_LOCK \
    -v $(realpath ./test_ch_data):/var/lib/clickhouse/ \
    -v $(realpath ./test_ch_logs):/var/log/clickhouse-server/ \
    -v $(realpath ./test/clickhouse-config/users.d):/etc/clickhouse-server/users.d:ro \
    --name hulation-test-clickhouse --ulimit nofile=262144:262144 clickhouse/clickhouse-server:26.4
