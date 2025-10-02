#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="hbase-gohbase-test"

echo -e "${YELLOW}Stopping HBase (gohbase) Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "hbase_example" ]; then
    echo "Stopping Go application..."
    pkill -f "hbase_example" 2>/dev/null || true
    rm -f hbase_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove HBase container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping HBase container (this may take a moment)..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ HBase container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

