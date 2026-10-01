# Todo Board contributor guide

Notes for coding agents working on this repository.

- Keep the host binding in `docker-compose.yml` on `127.0.0.1`.
- Keep the Go module standard-library-only.
- Never edit `data/tasks.json`. Exercise state through the store or the HTTP API.
- Every mutation path must validate, persist atomically and publish one SSE update.
- Keep `/api/ops` and each inbox file all-or-nothing.
- Keep the aggregate and project-grouped UI behavior consistent.
- Run `make test` after changes, plus live container and browser checks for anything user-visible.
