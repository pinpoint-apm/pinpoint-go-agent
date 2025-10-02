#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="oracle-test"

echo -e "${YELLOW}Stopping Oracle Database Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "oracle_example" ]; then
    echo "Stopping Go application..."
    pkill -f "oracle_example" 2>/dev/null || true
    rm -f oracle_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove Oracle container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping Oracle container (this may take a moment)..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ Oracle container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

