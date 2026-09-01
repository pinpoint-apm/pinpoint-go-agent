#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

ZOOKEEPER_CONTAINER="zookeeper-sarama-test"
KAFKA_CONTAINER="kafka-sarama-test"

echo -e "${YELLOW}Stopping Kafka (Sarama) Pinpoint Test Environment...${NC}"

# Stop Go applications if running
echo "Stopping Go applications..."
pkill -f "producer" 2>/dev/null || true
pkill -f "asyncproducer" 2>/dev/null || true
pkill -f "consumer" 2>/dev/null || true
pkill -f "consumer_group" 2>/dev/null || true
rm -f producer asyncproducer
echo -e "${GREEN}✓ Go applications stopped${NC}"

# Stop and remove Kafka container
if docker ps -a --format '{{.Names}}' | grep -q "^${KAFKA_CONTAINER}$"; then
    echo "Stopping Kafka container..."
    docker stop $KAFKA_CONTAINER 2>/dev/null || true
    docker rm $KAFKA_CONTAINER 2>/dev/null || true
    echo -e "${GREEN}✓ Kafka container stopped and removed${NC}"
fi

# Stop and remove Zookeeper container
if docker ps -a --format '{{.Names}}' | grep -q "^${ZOOKEEPER_CONTAINER}$"; then
    echo "Stopping Zookeeper container..."
    docker stop $ZOOKEEPER_CONTAINER 2>/dev/null || true
    docker rm $ZOOKEEPER_CONTAINER 2>/dev/null || true
    echo -e "${GREEN}✓ Zookeeper container stopped and removed${NC}"
fi

echo -e "${GREEN}Cleanup complete!${NC}"

