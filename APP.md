# Colin Architecture Notes

This file captures application-specific context that should stay stable across tasks. Replace placeholders with your real project details.

## Purpose

Describe what this application does and the core user outcomes it supports.

Colin is an automation tool

## System Boundaries

- Primary runtime(s): macOS
- External services: Linear and Codex App server
- Data stores: Linear metadata and git branches as metadata

## Repository Layout

- `internal/` - main application code
- `e2e/` - automated tests
- `docs/` - user/developer documentation

## Core Components

- `{{COMPONENT_1}}`: {{RESPONSIBILITY}}
- `{{COMPONENT_2}}`: {{RESPONSIBILITY}}
- `{{COMPONENT_3}}`: {{RESPONSIBILITY}}

## Architecture Rules

- Keep business logic in `{{BUSINESS_LOGIC_PATH}}`; avoid mixing it with transport/adapters.
- Prefer extension through existing abstractions before introducing new top-level modules.
- Record significant architecture tradeoffs in the active ExecPlan decision log.

## Local Development

- Install dependencies: `go mod download`
- Run app locally: `go run .`
- Run tests locally: `go test ./...`
- Lint/format checks: `go vet ./...`

## Operational Constraints

- Security and privacy requirements: {{SECURITY_NOTES}}
- Performance expectations: {{PERFORMANCE_NOTES}}
- Compatibility constraints: {{COMPATIBILITY_NOTES}}

## Change Checklist for Contributors

- Update this file when architecture, paths, or commands change.
- Keep examples and commands copy/paste ready.
- Ensure this file stays consistent with `README.md`, `WORKFLOW.md`, and `LANGUAGE.md`.
