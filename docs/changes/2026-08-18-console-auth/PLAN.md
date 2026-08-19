# Console authentication implementation

## Status

Implemented on 2026-08-19 across `unio-gateway`, `unio-admin`, and
`unio-console`. The development database is migrated through version 50.

## Scope

- Add a `console-server` HTTP process exposing the confirmed `/v1/auth/*` API.
- Store users and authentication metadata in PostgreSQL, with Redis for verification challenges, rate limits, and refresh sessions.
- Keep the internal `users.id` bigint primary key for database relations and add a unique `users.uid` UUID for public APIs and JWT `sub`.
- Organize Console authentication under `internal/app/consoleapi/auth`, `internal/service/console/auth`, and `sql/queries/console`.
- Do not add a PostgreSQL request-idempotency table in the first version; mutation requests are not retried automatically.
- Add the verification rate-limit runtime setting to the existing Admin settings registry.
- Update `unio-admin` to edit the authentication setting.
- Update `unio-console` authentication pages to call the API using secure cookies.
- Add `GET /v1/auth/me` to validate the access-token session and return the
  current active user.
- Restore browser sessions on Console startup, protect application routes, and
  refresh an expired access token once before returning to sign-in.
- Rate-limit password login failures by email plus IP and by IP without adding
  an account-wide lockout that another user could trigger.

## Verification

- Gateway: `gofmt`, `sqlc generate`, `go mod tidy`, and `go test ./...` pass.
- Admin: typecheck, lint, 121 tests, and production build pass.
- Console: typecheck, lint, 37 tests, and production build pass.
- Browser verification covers login and registration email-check loading plus
  password show/hide behavior, unauthenticated route protection, and the live
  `/v1/auth/me` plus CORS contract against the running local services.
- `git diff --check` passes in all three repositories.
