#!/bin/bash

set -e  # Exit on error

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR"

echo "================================================"
echo "Kafka (Sarama) + Go Example Runner"
echo "================================================"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Container names
ZOOKEEPER_CONTAINER="zookeeper-sarama-test"
KAFKA_CONTAINER="kafka-sarama-test"
PRODUCER_PORT="9023"
ASYNC_PRODUCER_PORT="9024"

# Check if Docker is running
if ! docker info > /dev/null 2>&1; then
    echo -e "${RED}Error: Docker is not running. Please start Docker first.${NC}"
    exit 1
fi

# Function to cleanup on exit
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"
    if [ ! -z "$PRODUCER_PID" ]; then
        echo "Stopping Producer (PID: $PRODUCER_PID)..."
        kill $PRODUCER_PID 2>/dev/null || true
    fi
    if [ ! -z "$ASYNC_PRODUCER_PID" ]; then
        echo "Stopping Async Producer (PID: $ASYNC_PRODUCER_PID)..."
        kill $ASYNC_PRODUCER_PID 2>/dev/null || true
    fi
}

trap cleanup EXIT INT TERM

# Step 1: Stop and remove existing containers if exist
echo -e "\n${YELLOW}[1/7] Checking for existing Kafka containers...${NC}"
if docker ps -a --format '{{.Names}}' | grep -q "^${KAFKA_CONTAINER}$"; then
    echo "Stopping and removing existing Kafka container..."
    docker stop $KAFKA_CONTAINER 2>/dev/null || true
    docker rm $KAFKA_CONTAINER 2>/dev/null || true
fi
if docker ps -a --format '{{.Names}}' | grep -q "^${ZOOKEEPER_CONTAINER}$"; then
    echo "Stopping and removing existing Zookeeper container..."
    docker stop $ZOOKEEPER_CONTAINER 2>/dev/null || true
    docker rm $ZOOKEEPER_CONTAINER 2>/dev/null || true
fi

# Step 2: Pull Docker images
echo -e "\n${YELLOW}[2/7] Pulling Docker images...${NC}"
docker pull confluentinc/cp-zookeeper:7.5.0
docker pull confluentinc/cp-kafka:7.5.0

# Step 3: Start Zookeeper
echo -e "\n${YELLOW}[3/7] Starting Zookeeper...${NC}"
docker run -d \
    --name $ZOOKEEPER_CONTAINER \
    -p 2181:2181 \
    -e ZOOKEEPER_CLIENT_PORT=2181 \
    -e ZOOKEEPER_TICK_TIME=2000 \
    confluentinc/cp-zookeeper:7.5.0

echo "Started Zookeeper on port 2181"
sleep 5

# Step 4: Start Kafka
echo -e "\n${YELLOW}[4/7] Starting Kafka...${NC}"
docker run -d \
    --name $KAFKA_CONTAINER \
    --link $ZOOKEEPER_CONTAINER:zookeeper \
    -p 9092:9092 \
    -e KAFKA_BROKER_ID=1 \
    -e KAFKA_ZOOKEEPER_CONNECT=zookeeper:2181 \
    -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:9092 \
    -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT \
    -e KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT \
    -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
    -e KAFKA_AUTO_CREATE_TOPICS_ENABLE=true \
    confluentinc/cp-kafka:7.5.0

echo "Started Kafka on port 9092"

# Wait for Kafka to be ready
echo -e "\n${YELLOW}[5/7] Waiting for Kafka to be ready...${NC}"
echo "This may take 20-30 seconds..."

MAX_RETRIES=60
RETRY_COUNT=0
echo -n "Checking Kafka health"
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if docker logs $KAFKA_CONTAINER 2>&1 | grep -q "started (kafka.server.KafkaServer)"; then
        echo -e " ${GREEN}✓${NC}"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo -n "."
    sleep 1
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo -e " ${RED}✗${NC}"
    echo -e "${RED}Error: Kafka failed to start${NC}"
    docker logs $KAFKA_CONTAINER
    exit 1
fi

echo -e "${GREEN}✓ Kafka is ready!${NC}"

# Additional wait to ensure Kafka is fully ready
sleep 3

# Step 6: Setup Pinpoint config if needed
echo -e "\n${YELLOW}[6/7] Checking Pinpoint configuration...${NC}"
PINPOINT_CONFIG="$HOME/tmp/pinpoint-config.yaml"
if [ ! -f "$PINPOINT_CONFIG" ]; then
    echo "Creating default Pinpoint config at $PINPOINT_CONFIG"
    mkdir -p "$HOME/tmp"
    cat > "$PINPOINT_CONFIG" << 'EOF'
enable: true
applicationName: GoKafkaTest
agentId: GoKafkaTestAgent
collector:
  host: localhost
  agentPort: 9991
  statPort: 9992
  spanPort: 9993
sampling:
  type: COUNTER
  counterRate: 1
EOF
    echo -e "${GREEN}✓ Created default config${NC}"
else
    echo -e "${GREEN}✓ Config file exists${NC}"
fi

# Step 7: Build and run Go applications
echo -e "\n${YELLOW}[7/7] Building and running Go applications...${NC}"

# Build producer
echo "Building producer..."
go build -o producer producer.go

# Build async producer
echo "Building asyncproducer..."
go build -o asyncproducer asyncproducer.go

# Run producer in background
echo "Starting Producer on http://localhost:$PRODUCER_PORT"
./producer &
PRODUCER_PID=$!
sleep 2

# Run async producer in background
echo "Starting Async Producer on http://localhost:$ASYNC_PRODUCER_PORT"
./asyncproducer &
ASYNC_PRODUCER_PID=$!
sleep 2

# Check if applications are running
if ps -p $PRODUCER_PID > /dev/null && ps -p $ASYNC_PRODUCER_PID > /dev/null; then
    echo -e "${GREEN}✓ Applications are running${NC}"
    echo "  - Producer PID: $PRODUCER_PID"
    echo "  - Async Producer PID: $ASYNC_PRODUCER_PID"
else
    echo -e "${RED}Error: Applications failed to start${NC}"
    exit 1
fi

# Display connection info
echo -e "\n${GREEN}================================================${NC}"
echo -e "${GREEN}Everything is ready!${NC}"
echo -e "${GREEN}================================================${NC}"
echo ""
echo "Kafka Infrastructure:"
echo "  - Zookeeper: localhost:2181"
echo "  - Kafka Broker: localhost:9092"
echo "  - Topics: go-sarama-test, go-kafka-test (auto-created)"
echo ""
echo "Go Applications:"
echo "  - Producer: http://localhost:$PRODUCER_PORT (PID: $PRODUCER_PID)"
echo "  - Async Producer: http://localhost:$ASYNC_PRODUCER_PORT (PID: $ASYNC_PRODUCER_PID)"
echo ""
echo "Test endpoints:"
echo "  curl http://localhost:$PRODUCER_PORT/save"
echo "  curl http://localhost:$ASYNC_PRODUCER_PORT/save_async"
echo ""
echo "Run consumer (in another terminal):"
echo "  cd $SCRIPT_DIR && go run consumer.go"
echo ""
echo "Run consumer_group (in another terminal):"
echo "  cd $SCRIPT_DIR && go run consumer_group.go"
echo ""
echo -e "${YELLOW}Press Ctrl+C to stop everything${NC}"
echo ""

# Keep the script running
wait $PRODUCER_PID

