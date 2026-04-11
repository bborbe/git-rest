---
status: approved
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:49Z"
---

<summary>
- The HTTP server in main.go sets only ReadHeaderTimeout, leaving ReadTimeout and WriteTimeout unset
- A missing ReadTimeout allows slow-body upload attacks (attacker sends POST body one byte at a time)
- A missing WriteTimeout allows slow-client attacks (attacker never reads the response)
- Both are classic Slowloris-style resource exhaustion vectors
- Adding ReadTimeout, WriteTimeout, and IdleTimeout closes these attack surfaces with minimal code change
</summary>

<objective>
Add `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` to the `http.Server` struct in `main.go` to prevent resource exhaustion from slow HTTP clients.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `main.go`: find the `http.Server` struct literal (search for `&http.Server{`)
</context>

<requirements>
1. In `main.go`, update the `http.Server` literal (search for `&http.Server{`) to add the three missing timeouts:
   ```go
   server := &http.Server{
       Addr:              *addr,
       Handler:           metricsMiddleware(mux),
       ReadHeaderTimeout: 10 * time.Second,
       ReadTimeout:       60 * time.Second,
       WriteTimeout:      60 * time.Second,
       IdleTimeout:       120 * time.Second,
   }
   ```
   `ReadTimeout` of 60s accommodates legitimate uploads up to 10 MB (the existing `maxBodyBytes` limit) at ~170 KB/s. `WriteTimeout` of 60s covers large file reads. `IdleTimeout` of 120s releases keep-alive connections that go idle.

2. No other changes are needed — no test changes are required since `main_test.go` only performs a compile check.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do not change the `ReadHeaderTimeout` value (10s)
- Use `time.Second` constants — not integer literals
</constraints>

<verification>
make precommit
</verification>
