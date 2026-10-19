#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CONTAINER_NAME="mysql-pinpoint-test"

echo -e "${YELLOW}Stopping MySQL Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "mysql_example" ]; then
    echo "Stopping Go application..."
    pkill -f "mysql_example" 2>/dev/null || true
    rm -f mysql_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove MySQL container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "Stopping MySQL container..."
    docker stop $CONTAINER_NAME 2>/dev/null || true
    docker rm $CONTAINER_NAME 2>/dev/null || true
    echo -e "${GREEN}✓ MySQL container stopped and removed${NC}"
else
    echo "MySQL container not found (already stopped)"
fi

echo -e "${GREEN}Cleanup complete!${NC}"


