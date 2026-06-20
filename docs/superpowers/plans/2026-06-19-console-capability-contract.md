# Console Capability Contract Closure

## Goal

Close the remaining P0-C15 traceability evidence by adding an end-to-end console capability contract test and the smallest backend/dashboard contract needed for operators to see state, evidence, and security boundaries.

## Scope

- Keep the existing API surface; prefer enriching the implemented dashboard response over introducing new routes.
- Cover setup, user console, admin console, dashboard, logs, API Key one-time secret behavior, and credential boundary checks.
- Keep protected pricing behavior read-only and use the protected pricing diff guard before staging and committing.

## Steps

1. Add a failing router test that exercises the console capability flow and expects dashboard readiness/dependency fields.
2. Implement dashboard readiness/dependency fields with the same local readiness helpers used by `/ready`.
3. Update API, Apifox, testing, and traceability docs for the new contract evidence.
4. Run targeted and full verification, then run the protected pricing diff guard.
5. Stage and commit the focused change.
