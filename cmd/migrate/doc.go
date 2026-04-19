// Command migrate applies the embedded SQL migrations against the MySQL
// database identified by MYSQL_DSN and exits.
//
// This is the same migration step the server runs at startup (see
// cmd/server), split out so it can run standalone in CI pre-checks,
// blue/green deploys, or to prime a fresh database for tests without
// starting the HTTP server.
//
// Exit codes:
//
//	0 — migrations are up to date (or no new migrations were needed)
//	1 — migration failed; stderr contains the error
//
// Usage:
//
//	export MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/contract_api?parseTime=true'
//	go run ./cmd/migrate
package main
