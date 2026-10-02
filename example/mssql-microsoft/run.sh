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

# Step 1: Start SQL Server and wait until it answers queries
echo -e "\n${YELLOW}[1/3] Starting SQL Server container (this may take 30-60 seconds)...${NC}"
docker compose up -d --wait --wait-timeout 300
echo -e "${GREEN}✓ SQL Server is ready!${NC}"

# Step 2: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[2/3] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'CFG'
enable: true
applicationName: GoMsSQLTest
collector:
  host: localhost
  agentPort: 9991
  statPort: 9992
  spanPort: 9993
sampling:
  type: COUNTER
  counterRate: 1
CFG
    echo -e "${GREEN}✓ Created default config${NC}"
else
    echo -e "${GREEN}✓ Config file exists${NC}"
fi

# Step 3: Build and run Go application
echo -e "\n${YELLOW}[3/3] Building and running Go application...${NC}"
go build -o mssql_example mssql_example.go
./mssql_example &
APP_PID=$!
sleep 2

if ps -p $APP_PID > /dev/null; then
    echo -e "${GREEN}✓ Application is running (PID: $APP_PID)${NC}"
else
    echo -e "${RED}Error: Application failed to start${NC}"
    exit 1
fi

echo -e "\n${GREEN}================================================${NC}"
echo -e "${GREEN}Everything is ready!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "SQL Server:"
echo "  - Host: localhost:1433"
echo "  - User: sa"
echo "  - Password: TestPass123"
echo "  - Database: TestDB (created on the first request)"
echo ""
echo "Go Application:"
echo "  - Running on: http://localhost:9021"
echo "  - PID: $APP_PID"
echo ""
echo "Test endpoints:"
echo "  curl http://localhost:9021/query"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

wait $APP_PID
