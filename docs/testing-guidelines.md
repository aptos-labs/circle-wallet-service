# Test Oracle Guidelines

Use these rules to avoid tests that pass without proving behavior.

## Core Principles

- Assert exact expected outcomes when possible.
- Avoid permissive checks like "status is not 401" unless that is the real requirement.
- Verify side effects, not only transport-level status codes.
- Ensure test names match what is actually verified.

## Handler Tests

- For mutation endpoints, assert:
  - response status/body;
  - submitted operation name and required role;
  - request payload mapping (JSON -> operation fields);
  - payload build function/shape for the operation.
- For validation failures, assert submitter is not called.

## Router and Middleware Tests

- Cover both branches for mode flags (for example `testingMode=true/false`).
- Assert exact status and behavior for each branch.

## E2E Test Prerequisites

- E2E tests run in strict mode when `CI=true` or `E2E_STRICT=true`.
- In strict mode, missing prerequisites (such as `aptos` CLI) must fail the run.
- Local non-strict runs may skip e2e if prerequisites are missing.
