#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "MS SQL Server + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container name
CONTAINER_NAME="mssql-test"
APP_PORT="9021"

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
echo -e "\n${YELLOW}[1/7] Checking for existing MS SQL Server container...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping and removing existing container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
fi

# Step 2: Pull Docker image
echo -e "\n${YELLOW}[2/7] Pulling Azure SQL Edge Docker image...${NC}"
docker pull mcr.microsoft.com/azure-sql-edge:latest

# Step 3: Start SQL Edge container
echo -e "\n${YELLOW}[3/7] Starting Azure SQL Edge container...${NC}"
docker run -d \
    --name $CONTAINER_NAME \
    -p 1433:1433 \
    -e ACCEPT_EULA=Y \
    -e SA_PASSWORD=TestPass123 \
    mcr.microsoft.com/azure-sql-edge:latest

echo "Started Azure SQL Edge on port 1433"

# Step 4: Wait for SQL Edge to be ready
echo -e "\n${YELLOW}[4/7] Waiting for Azure SQL Edge to be ready...${NC}"
echo "This may take 20-30 seconds (SQL Edge takes time to initialize)..."

MAX_RETRIES=60
RETRY_COUNT=0
echo -n "Checking SQL Edge health (looking for ready message in logs)"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker logs $CONTAINER_NAME 2>&1 | grep -q "SQL Server is now ready for client connections"; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 2
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: Azure SQL Edge failed to start${NC}"
    docker logs $CONTAINER_NAME
    exit 1
fi

echo -e "${GREEN}✓ Azure SQL Edge is ready!${NC}"

# Step 5: No need to create database explicitly, the application will handle it
echo -e "\n${YELLOW}[5/7] SQL Edge is ready for connections${NC}"
echo -e "${GREEN}✓ Skipping database creation (will be handled by application)${NC}"

# Additional wait to ensure SQL Edge is fully ready for connections
sleep 2

# Step 6: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[6/7] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoMsSQLTest
agentId: GoMsSQLTestAgent
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
echo "Building mssql_example..."
go build -o mssql_example mssql_example.go

# Run the application in background
echo "Starting application on http://localhost:$APP_PORT"
./mssql_example &
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
echo "Azure SQL Edge:"
echo "  - Host: localhost:1433"
echo "  - Database: TestDB"
echo "  - Username: sa"
echo "  - Password: TestPass123"
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

