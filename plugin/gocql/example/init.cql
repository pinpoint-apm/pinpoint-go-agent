-- Initialize Cassandra keyspace and table for gocql example

CREATE KEYSPACE IF NOT EXISTS example
WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};

USE example;

CREATE TABLE IF NOT EXISTS tweet (
    timeline text,
    id uuid,
    text text,
    PRIMARY KEY (timeline, id)
);

-- Insert test data
INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Hello from Pinpoint!');
INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Testing Cassandra with gocql');
INSERT INTO tweet (timeline, id, text) VALUES ('me', uuid(), 'Distributed database');

