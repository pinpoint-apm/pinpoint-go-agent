#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="redis-redigo-test"

echo -e "${YELLOW}Stopping Redis (Redigo) Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "redigo_example" ]; then
    echo "Stopping Go application..."
    pkill -f "redigo_example" 2>/dev/null || true
    rm -f redigo_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove Redis container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping Redis container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ Redis container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

