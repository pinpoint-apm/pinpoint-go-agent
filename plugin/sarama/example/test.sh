#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

PRODUCER_URL="http://localhost:9023"
ASYNC_PRODUCER_URL="http://localhost:9024"

echo "================================================"
echo "Testing Kafka (Sarama) Example Endpoints"
echo "================================================"

# Function to test endpoint
test_endpoint() {
    local url=$1
    local description=$2
    
    echo -e "\n${YELLOW}Testing: $description${NC}"
    echo "URL: $url"
    echo "Response:"
    
    response=$(curl -s -w "\n%{http_code}" "$url")
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" -eq 200 ]; then
        echo -e "${GREEN}✓ Success (HTTP $http_code)${NC}"
        echo "$body"
    else
        echo -e "${RED}✗ Failed (HTTP $http_code)${NC}"
        echo "$body"
    fi
}

# Check if producer is running
echo "Checking if Producer is running..."
if ! curl -s "$PRODUCER_URL/save" > /dev/null 2>&1; then
    echo -e "${RED}Error: Producer is not running at $PRODUCER_URL${NC}"
    echo "Please run './run.sh' first"
    exit 1
fi
echo -e "${GREEN}✓ Producer is running${NC}"

# Check if async producer is running
echo "Checking if Async Producer is running..."
if ! curl -s "$ASYNC_PRODUCER_URL/save_async" > /dev/null 2>&1; then
    echo -e "${RED}Error: Async Producer is not running at $ASYNC_PRODUCER_URL${NC}"
    echo "Please run './run.sh' first"
    exit 1
fi
echo -e "${GREEN}✓ Async Producer is running${NC}"

# Test Producer endpoint
test_endpoint "$PRODUCER_URL/save" "Producer - Send message to Kafka (Sync)"

# Test Async Producer endpoint
test_endpoint "$ASYNC_PRODUCER_URL/save_async" "Async Producer - Send messages to Kafka (Async)"

echo -e "\n${GREEN}================================================${NC}"
echo -e "${GREEN}Testing complete!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "Messages have been sent to Kafka topics:"
echo "  - go-sarama-test"
echo "  - go-kafka-test"
echo ""
echo "To consume messages, run in another terminal:"
echo "  cd $(dirname $0) && go run consumer.go"
echo ""
echo "Or run consumer group:"
echo "  cd $(dirname $0) && go run consumer_group.go"
echo ""
echo "You can also check Kafka topics directly:"
echo "  docker exec kafka-sarama-test kafka-topics --list --bootstrap-server localhost:9092"
echo "  docker exec kafka-sarama-test kafka-console-consumer --bootstrap-server localhost:9092 --topic go-sarama-test --from-beginning"

