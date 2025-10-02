#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="mssql-test"

echo -e "${YELLOW}Stopping MS SQL Server Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "mssql_example" ]; then
    echo "Stopping Go application..."
    pkill -f "mssql_example" 2>/dev/null || true
    rm -f mssql_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove SQL Server container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping MS SQL Server container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ MS SQL Server container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

