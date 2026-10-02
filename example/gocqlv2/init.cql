-- Initialize Cassandra keyspace and table for gocql/v2 example

CREATE KEYSPACE IF NOT EXISTS example
WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};

USE example;

CREATE TABLE IF NOT EXISTS tweet (
    timeline text,
    id uuid,
    text text,
    PRIMARY KEY (timeline, id)
);

-- Insert test data (fixed ids, so running this again changes nothing)
INSERT INTO tweet (timeline, id, text) VALUES ('me', 00000000-0000-0000-0000-000000000001, 'Hello from Pinpoint!');
INSERT INTO tweet (timeline, id, text) VALUES ('me', 00000000-0000-0000-0000-000000000002, 'Testing Cassandra with gocql/v2');
INSERT INTO tweet (timeline, id, text) VALUES ('me', 00000000-0000-0000-0000-000000000003, 'Distributed database');

