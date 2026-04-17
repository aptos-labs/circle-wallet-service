// Package store defines the transaction persistence interfaces and data types
// shared across the API handlers, submitter, poller, and webhook subsystems.
//
// Two interfaces are defined:
//   - [Store] for basic CRUD and status queries (used by handlers and poller).
//   - [Queue] which extends Store with atomic claim, sequence management, and
//     recovery operations (used by the submitter worker).
//
// The MySQL implementation lives in [store/mysql]. An in-memory implementation
// exists for unit tests.
package store
