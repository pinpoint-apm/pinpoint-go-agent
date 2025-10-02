#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "PostgreSQL + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="postgres-pinpoint-test"

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
echo -e "\n${YELLOW}[1/6] Checking for existing PostgreSQL container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Build Docker image
echo -e "\n${YELLOW}[2/6] Building PostgreSQL Docker image...${NC}"
docker build -t postgres-pinpoint-test .

# Step 3: Start PostgreSQL container
echo -e "\n${YELLOW}[3/6] Starting PostgreSQL container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 5432:5432 \
    postgres-pinpoint-test

# Step 4: Wait for PostgreSQL to be ready
echo -e "\n${YELLOW}[4/6] Waiting for PostgreSQL to be ready...${NC}"
echo "This may take 10-15 seconds..."

MAX_RETRIES=20
RETRY_COUNT=0
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker exec $CONTAINER_NAME pg_isready -U testuser -d testdb > /dev/null 2>&1; then
        echo -e "${GREEN}✓ PostgreSQL is ready!${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 2
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e "\n${RED}Error: PostgreSQL failed to start within expected time${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

# Additional wait to ensure PostgreSQL is fully ready
sleep 2

# Step 5: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[5/6] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoPgSqlTest
agentId: GoPgSqlTestId
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
echo "Building pgsql_example..."
go build -o pgsql_example pgsql_example.go

# Run the application in background
echo "Starting application on http://localhost:9002"
./pgsql_example &
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
echo "PostgreSQL Server:"
echo "  - Host: localhost:5432"
echo "  - User: testuser"
echo "  - Password: p123"
echo "  - Database: testdb"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:9002"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoints:"
echo "  curl http://localhost:9002/tableCount"
echo "  curl http://localhost:9002/query"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

