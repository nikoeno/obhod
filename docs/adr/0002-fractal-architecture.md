# ADR 0002: Fractal architecture

Status: accepted

## Decision

Code is a tree of modules with an identical shape at every depth. Placement rules: a module used by one module lives inside it; a module used by two or more lives in the `lib/` of their lowest common ancestor. Import rules allow only direct children and ancestor `lib/` roots (plus entry-point, lab and test-only exceptions). The full rules are in `BOOTSTRAP.md` section 1 and `AGENTS.md`.

## Why

- Each module is testable and replaceable on its own.
- An agent can work on one subtree without touching the rest.
- Shared code is visible in one place (`lib/`), not scattered.

## Enforcement

`arch_test.go` reads the `API.md` headers, checks every in-repo import against the rules and checks the declared Consumers, Children and Uses libs. Nested Go `internal/` folders were rejected because they cannot express the ancestor-`lib/` and test-only rules and hide the shape from readers.
