# Scripts

Three shell scripts. Each one starts the server it talks to, on a port the
kernel hands out, against a database it creates and drops. None of them adopt a
server they did not start, and none of them look for a process by port and kill
it — an earlier version did exactly that on 8080, which on a developer machine
is usually Docker.

The Go suite is where the business rules are tested: `make test` runs 41
packages against real Postgres. These scripts cover what a Go test structurally
cannot — that the compiled binary, started the way the deployment starts it,
migrates an empty database and then works.

## smoke-test.sh — a trading day against the built binary

```bash
make db-up        # once
make smoke
```

Builds `./cmd/server`, points it at a throwaway database, and runs a shop's day
over HTTP: set up a store and a terminal, create a product sold both loose and
by the 25 kg sack, receive four sacks on a purchase order, sell loose and bulk
on one bill, take a credit sale and settle it, void a bill, then check that the
dashboard, the settlement and GSTR-1 all agree. Figures are asserted exactly,
to the paisa.

49 assertions, about a minute.

## verify-security.sh — the hardening, still holding

```bash
make verify-security
```

Each check corresponds to a defect that was once exploitable: a placeholder JWT
secret accepted in release mode, seeded demo logins, SQL injected through a
limit parameter, an endpoint that ignored store scoping, a client naming its own
invoice status or total, a password reaching the audit log. Run it after
touching config, auth, the audit middleware or the store-scope helper.

## e2e — deleted

`comprehensive-e2e-test.sh` was 1,000 lines of assertions against the
pre-Postgres API. By the time anyone ran it again it reported 199 failures on a
healthy system, which is worse than having no test: it teaches everyone to
ignore the output. What it covered is covered by `internal/http`'s router tests
and by smoke-test.sh above.
