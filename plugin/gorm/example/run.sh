#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "MySQL (GORM) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="mysql-gorm-test"
APP_PORT="9019"

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
echo -e "\n${YELLOW}[1/6] Checking for existing MySQL container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Pull Docker image
echo -e "\n${YELLOW}[2/6] Pulling MySQL Docker image...${NC}"
docker pull mysql:8.0

# Step 3: Start MySQL container
echo -e "\n${YELLOW}[3/6] Starting MySQL container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 3306:3306 \
    -e MYSQL_ROOT_PASSWORD=p123 \
    -e MYSQL_DATABASE=testdb \
    mysql:8.0

echo "Started MySQL server on port 3306"

# Step 4: Wait for MySQL to be ready
echo -e "\n${YELLOW}[4/6] Waiting for MySQL to be ready...${NC}"
echo "This may take 10-20 seconds..."

MAX_RETRIES=30
RETRY_COUNT=0
echo -n "Checking MySQL health"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker exec $CONTAINER_NAME mysqladmin ping -h localhost -uroot -pp123 --silent > /dev/null 2>&1; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 1
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: MySQL failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ MySQL is ready!${NC}"

# Additional wait to ensure MySQL is fully ready for connections
sleep 2

# Step 5: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[5/6] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoGormTest
agentId: GoGormTestAgent
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
echo "Building gorm_example..."
go build -o gorm_example gorm_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./gorm_example &
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
echo "MySQL Server:"
echo "  - Host: 127.0.0.1:3306"
echo "  - Database: testdb"
echo "  - Username: root"
echo "  - Password: p123"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:$APP_PORT/gormquery"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

