#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

BASE_URL="http://localhost:9016"

echo "================================================"
echo "Testing HBase (gohbase) Example Endpoint"
echo "================================================"

# Function to test endpoint
test_endpoint() {
    local endpoint=$1
    local description=$2
    
    echo -e "\n${YELLOW}Testing: $description${NC}"
    echo "URL: $BASE_URL$endpoint"
    echo "Response:"
    
    response=$(curl -s -w "\n%{http_code}" "$BASE_URL$endpoint")
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | head -n-1)
    
    if [ "$http_code" -eq 200 ]; then
        echo -e "${GREEN}✓ Success (HTTP $http_code)${NC}"
    else
        echo -e "${RED}✗ Failed (HTTP $http_code)${NC}"
    fi
    
    if [ ! -z "$body" ]; then
        echo "Body: $body"
    fi
}

# Check if server is running
echo "Checking if server is running..."
if ! curl -s "$BASE_URL/hbase" > /dev/null 2>&1; then
    echo -e "${RED}Error: Server is not running at $BASE_URL${NC}"
    echo "Please run './run.sh' first"
    exit 1
fi

echo -e "${GREEN}✓ Server is running${NC}"

# Test endpoint
test_endpoint "/hbase" "HBase Operations (PUT, GET, SCAN)"

echo -e "\n${GREEN}================================================${NC}"
echo -e "${GREEN}Testing complete!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "Check the console output of run.sh for HBase operation results"
echo ""
echo "You can also query HBase directly:"
echo "  docker exec -it hbase-gohbase-test hbase shell"
echo "  scan 'table'"
echo "  get 'table', 'row1'"

