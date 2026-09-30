#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="postgres-pinpoint-test"

echo -e "${YELLOW}Stopping PostgreSQL Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "pgsql_example" ]; then
    echo "Stopping Go application..."
    pkill -f "pgsql_example" 2>/dev/null || true
    rm -f pgsql_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove PostgreSQL container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping PostgreSQL container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ PostgreSQL container stopped and removed${NC}"
else
    echo "PostgreSQL container not found (already stopped)"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

