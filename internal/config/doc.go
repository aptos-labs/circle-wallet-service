// Package config loads application configuration from a YAML file (default
// config.yaml) with environment variable overrides. A .env file is also
// loaded via godotenv for local development convenience.
//
// Precedence: environment variable > YAML file > built-in default.
//
// Call [Load] at startup to get a validated [Config]. Accessor methods on
// Config are used throughout the codebase to read settings.
package config
