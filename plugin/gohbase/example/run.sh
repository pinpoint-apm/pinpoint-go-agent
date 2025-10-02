#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "HBase (gohbase) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="hbase-gohbase-test"
APP_PORT="9016"

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
echo -e "\n${YELLOW}[1/7] Checking for existing HBase container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Build Docker image
echo -e "\n${YELLOW}[2/7] Pulling HBase Docker image...${NC}"
docker pull dajobe/hbase

# Step 3: Start HBase container
echo -e "\n${YELLOW}[3/7] Starting HBase container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    --hostname hbase \
    -p 2181:2181 \
    -p 16000:16000 \
    -p 16010:16010 \
    -p 16020:16020 \
    -p 16030:16030 \
    dajobe/hbase

echo "Started HBase server"
echo "  - Zookeeper: 2181"
echo "  - Master: 16000"
echo "  - RegionServer: 16020"

# Step 4: Wait for HBase to be ready
echo -e "\n${YELLOW}[4/7] Waiting for HBase to be ready...${NC}"
echo "This may take 60-90 seconds (HBase takes time to initialize)..."

MAX_RETRIES=60
RETRY_COUNT=0
echo -n "Checking HBase status"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker exec $CONTAINER_NAME bash -c "echo 'status' | hbase shell -n 2>/dev/null | grep -q 'active master'" 2>/dev/null; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 3
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: HBase failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ HBase is ready!${NC}"

# Additional wait to ensure HBase is fully ready
sleep 5

# Step 5: Initialize HBase table
echo -e "\n${YELLOW}[5/7] Initializing HBase table...${NC}"
docker exec $CONTAINER_NAME bash -c "echo \"create 'table', 'cf'\" | hbase shell -n" > /dev/null 2>&1
echo "Created table 'table' with column family 'cf'"

# Insert test data
docker exec $CONTAINER_NAME bash -c "echo \"put 'table', 'row1', 'cf:a', 'value1'\" | hbase shell -n" > /dev/null 2>&1
docker exec $CONTAINER_NAME bash -c "echo \"put 'table', '7row2', 'cf:a', 'value2'\" | hbase shell -n" > /dev/null 2>&1
docker exec $CONTAINER_NAME bash -c "echo \"put 'table', '7row3', 'cf:a', 'value3'\" | hbase shell -n" > /dev/null 2>&1
echo "Inserted test data"
echo -e "${GREEN}✓ HBase initialized!${NC}"

# Step 6: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[6/7] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoHbaseTest
agentId: GoHbaseTestAgent
collector:
  host: localhost
  agentPort: 9991
  statPort: 9992
  spanPort: 9993
sampling:
  type: COUNTER
  counterRate: 1
logLevel: debug
EOF
    echo -e "${GREEN}✓ Created default config${NC}"
else
    echo -e "${GREEN}✓ Config file exists${NC}"
fi

# Step 7: Build and run Go application
echo -e "\n${YELLOW}[7/7] Building and running Go application...${NC}"

# Build the application
echo "Building hbase_example..."
go build -o hbase_example hbase_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./hbase_example &
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
echo "HBase Server:"
echo "  - Zookeeper: localhost:2181"
echo "  - Master: localhost:16000"
echo "  - RegionServer: localhost:16020"
echo "  - Master Web UI: http://localhost:16010"
echo "  - Table: table"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:$APP_PORT/hbase"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

