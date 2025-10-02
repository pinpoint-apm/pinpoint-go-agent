#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "Oracle Database XE + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="oracle-test"
APP_PORT="9022"

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
echo -e "\n${YELLOW}[1/7] Checking for existing Oracle container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Pull Docker image
echo -e "\n${YELLOW}[2/7] Pulling Oracle XE Docker image...${NC}"
echo "Note: This may take a while (image is ~2GB)"
docker pull container-registry.oracle.com/database/express:21.3.0-xe

# Step 3: Start Oracle container
echo -e "\n${YELLOW}[3/7] Starting Oracle XE container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 1521:1521 \
    -e ORACLE_PWD=tiger \
    -e ORACLE_CHARACTERSET=AL32UTF8 \
    container-registry.oracle.com/database/express:21.3.0-xe

echo "Started Oracle XE on port 1521"

# Step 4: Wait for Oracle to be ready
echo -e "\n${YELLOW}[4/7] Waiting for Oracle Database to be ready...${NC}"
echo "This may take 2-5 minutes (Oracle takes time to initialize)..."
echo "Please be patient..."

MAX_RETRIES=150
RETRY_COUNT=0
echo -n "Checking Oracle health"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker logs $CONTAINER_NAME 2>&1 | grep -q "DATABASE IS READY TO USE"; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 2
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: Oracle Database failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ Oracle Database is ready!${NC}"

# Additional wait to ensure Oracle is fully ready for connections
sleep 5

# Step 5: Create BONUS table
echo -e "\n${YELLOW}[5/7] Creating BONUS table...${NC}"
docker exec $CONTAINER_NAME bash -c "echo \"CREATE TABLE scott.BONUS (ENAME VARCHAR2(10), JOB VARCHAR2(9), SAL NUMBER, COMM NUMBER);\" | sqlplus -s scott/tiger@//localhost:1521/xe" > /dev/null 2>&1 || true
echo -e "${GREEN}✓ Table creation attempted (may already exist)${NC}"

# Step 6: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[6/7] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoOracleTest
agentId: GoOracleTestAgent
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
echo "Building oracle_example..."
go build -o oracle_example oracle_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./oracle_example &
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
echo "Oracle Database XE:"
echo "  - Host: localhost:1521"
echo "  - Service: xe"
echo "  - Username: scott"
echo "  - Password: tiger"
echo "  - Table: BONUS"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:$APP_PORT"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoint:"
echo "  curl http://localhost:$APP_PORT/query"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running and show logs
wait $APP_PID

