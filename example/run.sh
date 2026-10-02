#!/usr/bin/env bash
# Runs an example that needs a database: example/run.sh <name>, e.g. mysql.
#
# Starts example/<name>/docker-compose.yml and waits for its healthcheck,
# which also loads the example's data where it needs any, then runs the
# example in the foreground; its source has the port and the handlers. Like
# every example, it reads ~/tmp/pinpoint-config.yaml when there is one and the
# PINPOINT_GO_* variables, e.g. PINPOINT_GO_COLLECTOR_HOST.
#
# The database keeps running after Ctrl-C, for the next run. Remove it, data
# included, with: docker compose -f example/<name>/docker-compose.yml down -v
set -euo pipefail
cd "$(dirname "$0")/${1:?usage: example/run.sh <name>, e.g. mysql}"
docker compose up -d --wait
exec go run .
