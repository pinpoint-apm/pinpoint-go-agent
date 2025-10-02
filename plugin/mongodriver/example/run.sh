#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "MongoDB (mongodriver) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="mongodb-mongodriver-test"
APP_PORT="9020"

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
echo -e "\n${YELLOW}[1/6] Checking for existing MongoDB container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Pull Docker image
echo -e "\n${YELLOW}[2/6] Pulling MongoDB Docker image...${NC}"
docker pull mongo:6.0

# Step 3: Start MongoDB container
echo -e "\n${YELLOW}[3/6] Starting MongoDB container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 27017:27017 \
    -e MONGO_INITDB_DATABASE=testdb \
    mongo:6.0

echo "Started MongoDB server on port 27017"

# Step 4: Wait for MongoDB to be ready
echo -e "\n${YELLOW}[4/6] Waiting for MongoDB to be ready...${NC}"
echo "This may take 10-20 seconds..."

MAX_RETRIES=30
RETRY_COUNT=0
echo -n "Checking MongoDB health"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker exec $CONTAINER_NAME mongosh --eval "db.adminCommand('ping')" > /dev/null 2>&1; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 1
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: MongoDB failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ MongoDB is ready!${NC}"

# Additional wait to ensure MongoDB is fully ready for connections
sleep 2

# Step 5: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[5/6] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoMongoTest
agentId: GoMongoTestAgent
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
echo "Building mongo_example..."
go build -o mongo_example mongo_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./mongo_example &
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
echo "MongoDB Server:"
echo "  - Host: localhost:27017"
echo "  - Database: testdb"
echo "  - Collection: example"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:$APP_PORT/mongo"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

