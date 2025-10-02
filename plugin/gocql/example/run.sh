#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "Cassandra (gocql) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="cassandra-gocql-test"
APP_PORT="9015"

# Check if Docker is running
if ! docker info > /dev/null 2>&1; then
    echo -e "${RED}Error: Docker is not running. Please start Docker first.${NC}"
    exit 1
fi

# Function to cleanup on exit
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"
    if [ ! -z "$APP_PID" ]; then
        echo "Stopping Go application (PID: $APP_PID)..."
        kill $APP_PID 2>/dev/null || true
    fi
}

trap cleanup EXIT INT TERM

# Step 1: Stop and remove existing container if exists
echo -e "\n${YELLOW}[1/7] Checking for existing Cassandra container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Build Docker image
echo -e "\n${YELLOW}[2/7] Building Cassandra Docker image...${NC}"
docker build -t cassandra-gocql-test .

# Step 3: Start Cassandra container
echo -e "\n${YELLOW}[3/7] Starting Cassandra container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 9042:9042 \
    -e CASSANDRA_CLUSTER_NAME=TestCluster \
    -e CASSANDRA_DC=dc1 \
    -e CASSANDRA_RACK=rack1 \
    cassandra-gocql-test

echo "Started Cassandra server on port 9042"

# Step 4: Wait for Cassandra to be ready
echo -e "\n${YELLOW}[4/7] Waiting for Cassandra to be ready...${NC}"
echo "This may take 60-90 seconds (Cassandra takes time to initialize)..."

MAX_RETRIES=60
RETRY_COUNT=0
echo -n "Checking Cassandra status"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker exec $CONTAINER_NAME nodetool status 2>/dev/null | grep -q "UN"; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 3
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: Cassandra failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ Cassandra is ready!${NC}"

# Additional wait to ensure Cassandra is fully ready
sleep 5

# Wait for CQL to be ready
echo -e "\n${YELLOW}Waiting for CQL interface to be ready...${NC}"
RETRY_COUNT=0
MAX_CQL_RETRIES=30
echo -n "Checking CQL connection"
while [ $RETRY_COUNT -lt $MAX_CQL_RETRIES ]; do
    if docker exec $CONTAINER_NAME cqlsh -e "DESCRIBE KEYSPACES" > /dev/null 2>&1; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 2
done

if [ $RETRY_COUNT -eq $MAX_CQL_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: CQL interface failed to start${NC}"
    exit 1
fi

# Step 5: Initialize Cassandra schema and data
echo -e "\n${YELLOW}[5/7] Initializing Cassandra schema and data...${NC}"
docker exec $CONTAINER_NAME cqlsh -e "CREATE KEYSPACE IF NOT EXISTS example WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};" > /dev/null 2>&1
echo "Created keyspace 'example'"

docker exec $CONTAINER_NAME cqlsh -k example -e "CREATE TABLE IF NOT EXISTS tweet (timeline text, id uuid, text text, PRIMARY KEY (timeline, id));" > /dev/null 2>&1
echo "Created table 'tweet'"

docker exec $CONTAINER_NAME cqlsh -k example -e "INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Hello from Pinpoint!');" > /dev/null 2>&1
docker exec $CONTAINER_NAME cqlsh -k example -e "INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Testing Cassandra with gocql');" > /dev/null 2>&1
docker exec $CONTAINER_NAME cqlsh -k example -e "INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Distributed database');" > /dev/null 2>&1
echo "Inserted test data"
echo -e "${GREEN}✓ Cassandra initialized!${NC}"

# Step 6: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[6/7] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoCassandraTest
agentId: GoCassandraTestAgent
collector:
  host: localhost
  agentPort: 9991
  statPort: 9992
  spanPort: 9993
sampling:
  type: COUNTER
  counterRate: 1
EOF
    echo -e "${GREEN}✓ Created default config${NC}"
else
    echo -e "${GREEN}✓ Config file exists${NC}"
fi

# Step 7: Build and run Go application
echo -e "\n${YELLOW}[7/7] Building and running Go application...${NC}"

# Build the application
echo "Building gocql_example..."
go build -o gocql_example gocql_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./gocql_example &
APP_PID=$!

# Wait a bit for the server to start
sleep 2

# Check if the application is running
if ps -p $APP_PID > /dev/null; then
    echo -e "${GREEN}✓ Application is running (PID: $APP_PID)${NC}"
else
    echo -e "${RED}Error: Application failed to start${NC}"
    exit 1
fi

# Display connection info
echo -e "\n${GREEN}================================================${NC}"
echo -e "${GREEN}Everything is ready!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "Cassandra Server:"
echo "  - Host: 127.0.0.1:9042"
echo "  - Keyspace: example"
echo "  - Table: tweet"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:$APP_PORT/cassandra"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

