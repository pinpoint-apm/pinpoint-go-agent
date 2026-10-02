#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo -e "${YELLOW}Stopping MS SQL Server Pinpoint Test Environment...${NC}"

# Stop Go application if running
if [ -f "mssql_example" ]; then
    echo "Stopping Go application..."
    pkill -f "mssql_example" 2>/dev/null || true
    rm -f mssql_example
    echo -e "${GREEN}✓ Go application stopped${NC}"
fi

# Stop and remove SQL Server container
docker compose down -v
echo -e "${GREEN}Cleanup complete!${NC}"
