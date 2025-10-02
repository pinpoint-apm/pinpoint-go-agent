#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME_1="redis-goredisv8-test-1"
CONTAINER_NAME_2="redis-goredisv8-test-2"

echo -e "${YELLOW}Stopping Redis (go-redis v8) Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "redisv8" ]; then
    echo "Stopping Go application..."
    pkill -f "redisv8" 2>/dev/null || true
    rm -f redisv8
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove Redis containers
for container in $CONTAINER_NAME_1 $CONTAINER_NAME_2; do
    if docker ps -a --format '{{.Names}}' | grep -q "^${container}$"; then
        echo "Stopping Redis container: $container..."
        docker stop $container 2>/dev/null || true
        docker rm $container 2>/dev/null || true
        echo -e "${GREEN}✓ $container stopped and removed${NC}"
    fi
done

echo -e "${GREEN}Cleanup complete!${NC}"

