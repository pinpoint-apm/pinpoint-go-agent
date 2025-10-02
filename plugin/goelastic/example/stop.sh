#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="elasticsearch-goelastic-test"

echo -e "${YELLOW}Stopping Elasticsearch (goelastic) Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "goelastic" ]; then
    echo "Stopping Go application..."
    pkill -f "goelastic" 2>/dev/null || true
    rm -f goelastic
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove Elasticsearch container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping Elasticsearch container (this may take a moment)..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ Elasticsearch container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

