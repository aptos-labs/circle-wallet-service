// Package db handles MySQL database setup: embedded SQL migrations and
// connection pool initialization. Migrations are applied automatically on
// server startup via [Migrate], and the connection pool is opened via [Open].
package db
