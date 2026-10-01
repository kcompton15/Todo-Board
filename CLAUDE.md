# Todo Board contributor guide

Read `AGENTS.md` and `README.md` before changing this project. Keep the service standard-library-only and bound to localhost, keep persistence atomic, keep inbox batches all-or-nothing, and keep both UI modes working. Never edit `data/tasks.json` directly. Run `make test` plus relevant live container and browser checks after changes.
