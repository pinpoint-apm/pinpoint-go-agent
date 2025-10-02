#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "Redis (go-redis v9) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container names
CONTAINER_NAME_1="redis-goredisv9-test-1"
CONTAINER_NAME_2="redis-goredisv9-test-2"
APP_PORT="9012"

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

# Step 1: Stop and remove existing containers if exist
echo -e "\n${YELLOW}[1/6] Checking for existing Redis containers...${NC}"
for container in $CONTAINER_NAME_1 $CONTAINER_NAME_2; do
    if docker ps -a --format '{{.Names}}' | grep -q "^${container}$"; then
        echo "Stopping and removing existing container: $container"
        docker stop $container 2>/dev/null || true
        docker rm $container 2>/dev/null || true
    fi
done

# Step 2: Build Docker image
echo -e "\n${YELLOW}[2/6] Building Redis Docker image...${NC}"
docker build -t redis-goredisv9-test .

# Step 3: Start Redis containers
echo -e "\n${YELLOW}[3/6] Starting Redis containers...${NC}"

# Start Redis 1 (port 6379)
docker run -d \
    --name $CONTAINER_NAME_1 \
    -p 6379:6379 \
    redis-goredisv9-test \
    redis-server --appendonly yes

echo "Started Redis server 1 on port 6379"

# Start Redis 2 (port 6380)
docker run -d \
    --name $CONTAINER_NAME_2 \
    -p 6380:6379 \
    redis-goredisv9-test \
    redis-server --appendonly yes

echo "Started Redis server 2 on port 6380"

# Step 4: Wait for Redis to be ready
echo -e "\n${YELLOW}[4/6] Waiting for Redis servers to be ready...${NC}"

MAX_RETRIES=15
for port in 6379 6380; do
    RETRY_COUNT=0
    echo -n "Checking Redis on port $port"
    while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
        if docker exec $CONTAINER_NAME_1 redis-cli -p 6379 ping > /dev/null 2>&1 || \
           docker exec $CONTAINER_NAME_2 redis-cli ping > /dev/null 2>&1; then
            echo -e " ${GREEN}✓${NC}"
            break
        fi
        RETRY_COUNT=$((RETRY_COUNT+1))
        echo -n "."
        sleep 1
    done
    
    if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
        echo -e " ${RED}✗${NC}"
        echo -e "${RED}Error: Redis on port $port failed to start${NC}"
        exit 1
    fi
done

echo -e "${GREEN}✓ All Redis servers are ready!${NC}"

# Additional wait to ensure Redis is fully ready
sleep 1

# Step 5: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[5/6] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoRedisv9Test
agentId: GoRedisv9TestAgent
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

# Step 6: Build and run Go application
echo -e "\n${YELLOW}[6/6] Building and running Go application...${NC}"

# Build the application
echo "Building redisv9..."
go build -o redisv9 redisv9.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./redisv9 &
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
echo "Redis Servers:"
echo "  - Redis 1: localhost:6379 (single mode)"
echo "  - Redis 2: localhost:6380 (cluster mode)"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoints:"
echo "  curl http://localhost:$APP_PORT/redis"
echo "  curl http://localhost:$APP_PORT/rediscluster"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

