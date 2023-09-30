package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/pppgxv5"
)

func LoadDB() *pgxpool.Pool {
	ctx := context.Background()

	urlExample := "postgres://username:password@localhost:5432/database_name"
	config, err := pgxpool.ParseConfig(urlExample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to parse connection config: %v\n", err)
		os.Exit(1)
	}

	config.ConnConfig.Tracer = pppgxv5.NewTracerPgx()

	dbpool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to create connection pool: %v\n", err)
		os.Exit(1)
	}

	rows, err := dbpool.Query(ctx, "select 1")
	if err != nil {
		panic(err)
	}

	if err := rows.Err(); err != nil {
		panic(err)
	}

	log.Println("successfully connected to db")

	return dbpool
}
