#!/usr/bin/env bash
# Runs the distributed tracing demo locally: MySQL + server (:8081) + proxy (:8080).
#
#   PINPOINT_GO_COLLECTOR_HOST=my-collector ./run.sh
#
# Starts a MySQL container unless one is already listening on 3306 (override
# the connection with MYSQL_DSN). Generate traffic with:
#   curl http://localhost:8080/api/members
# Stop with Ctrl-C; the apps and the container are stopped together.
set -euo pipefail

cd "$(dirname "$0")/.."

container=""
if ! nc -z 127.0.0.1 3306 2>/dev/null; then
    echo "starting MySQL container..."
    container=$(docker run -d --rm -p 3306:3306 \
        -e MYSQL_ROOT_PASSWORD=p123 -e MYSQL_DATABASE=testdb mysql:8.0)
    until docker exec "$container" mysqladmin ping -h localhost -uroot -pp123 --silent 2>/dev/null; do
        sleep 2
    done
fi

# The apps are built rather than `go run`, so that stopping them here really
# stops them: `go run` spawns the binary as a child of its own.
bin=$(mktemp -d)
cleanup() {
    kill $(jobs -p) 2>/dev/null || true
    [[ -n $container ]] && docker stop "$container" >/dev/null
    rm -rf "$bin"
}
# Signals need their own trap: an untrapped fatal signal ends bash without
# running the EXIT trap, and a handled one resumes the `wait` below.
trap cleanup EXIT
trap 'exit 130' INT TERM

go build -o "$bin/" ./distributed/server ./distributed/proxy
"$bin/server" &
"$bin/proxy" &
wait
