---
name: codecov-readme-design
description: Add Codecov coverage reporting to CI and update README to reflect current repo state
metadata:
  type: project
---

# Codecov + README Accuracy

## Codecov Integration

### CI change (`.github/workflows/ci.yml` — `test` job)

Replace the existing test command with one that emits a coverage profile:

```yaml
- name: Run unit tests
  run: go test -count=1 -race -coverprofile=coverage.out -covermode=atomic ./...

- uses: codecov/codecov-action@v5
  with:
    files: coverage.out
    fail_ci_if_error: true
```

Scope: unit test job only (not smoke, not e2e). The `test` job already spins up MySQL, so all packages including `store/mysql` are exercised.

### Codecov config (`.codecov.yml`)

```yaml
coverage:
  status:
    project:
      default:
        informational: true   # overall % is informational; won't fail PRs
    patch:
      default:
        target: auto          # new code in a PR must not drop coverage
```

`patch` is the primary signal — new lines added in a PR should be tested. `project` overall is informational since the repo has integration-heavy paths that are hard to unit-test.

### README badge

Add a Codecov badge to the top of `README.md` pointing to the repo's Codecov page.

---

## README Accuracy Updates

### 1. Codecov badge

Add at top of file.

### 2. Quick Start — Step 4 (Configure)

Mention `.env.example` as the template to copy, since `make help` already references it but the README does not.

### 3. Configuration — Aptos section

Add two missing env vars:

| Variable | Default | Description |
|----------|---------|-------------|
| `SIMULATE_BEFORE_SUBMIT` | `true` | Run `/transactions/simulate` between build and sign to catch VM errors early |
| `CALIBRATE_GAS_FROM_SIMULATION` | `true` | Use simulation's `gas_used` to right-size `max_gas_amount` before signing |

### 4. Architecture tree — additions

- `cmd/migrate/` — standalone migration runner (apply schema without starting server)
- `internal/archive/` — background archiver for purging aged terminal rows

### 5. Architecture prose — Archiver

Add a short paragraph explaining the two-phase sweep (idempotency key nulling + row deletion), batch loop to avoid long lock holds, and that it's optional (gated by `ArchiveConfig.Enabled`).

### 6. Architecture prose — Pre-submit simulation

Add a short paragraph on `internal/aptos/client.go` (simulation helpers): runs `/transactions/simulate` before signing to catch Move VM errors early; optionally calibrates `max_gas_amount` from the simulation result; classifies transient network errors to avoid unnecessary retries.

---

## Out of Scope

- No changes to E2E or smoke workflows
- No restructuring of existing README sections
- No new make targets
