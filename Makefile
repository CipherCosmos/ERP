.PHONY: backend frontend smoke clean-testdb

# API (creates ./backend/data/ if needed; seeds on first run)
backend:
	cd backend && mkdir -p data && go run ./cmd/server

frontend:
	cd frontend && npm start

# Requires backend on 8080 (or set GBOS_API, e.g. http://127.0.0.1:18081/api/v1)
smoke:
	bash scripts/smoke-test.sh

clean-testdb:
	rm -f backend/data/test-seed.db
