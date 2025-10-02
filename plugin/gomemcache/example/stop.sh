#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="memcached-gomemcache-test"

echo -e "${YELLOW}Stopping Memcached (gomemcache) Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "gomemcache_example" ]; then
    echo "Stopping Go application..."
    pkill -f "gomemcache_example" 2>/dev/null || true
    rm -f gomemcache_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove Memcached container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping Memcached container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ Memcached container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

