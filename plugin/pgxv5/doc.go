// Package pppgxv5 instruments the jackc/pgx/v5 package (https://github.com/jackc/pgx).
//
// This package instruments the jackc/pgx/v5.
// Use the NewTracer as the pgx.ConnConfig.Tracer.
//
//	cfg, err := pgx.ParseConfig(connUrl)
//	cfg.Tracer = pppgxv5.NewTracer()
//	conn, err := pgx.ConnectConfig(context.Background(), cfg)
//
// It is necessary to pass the context containing the pinpoint.Tracer to pgx calls.
//
// This package instruments the database/sql driver of postgres calls also.
// Use this package's driver in place of the postgres driver.
//
//	db, err := sql.Open("pgxv5-pinpoint", connUrl)
//
// It is necessary to pass the context containing the pinpoint.Tracer to all exec and query methods on SQL driver.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	row := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_tables")
package pppgxv5
