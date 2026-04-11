# GBOS (Grocery Business Operating System)

Monorepo: **Go API** (`backend/`), **React POS** (`frontend/`), optional **Flutter** (`mobile/`).

## Quick test (local)

1. **API** — from repo root:

   ```bash
   make backend
   ```

   Defaults: `http://127.0.0.1:8080`, SQLite at `backend/data/gbos.db`. The data directory is created automatically.

2. **Smoke checks** (in another terminal, with API running on the default port):

   ```bash
   make smoke
   ```

   If the API uses another port: `GBOS_API=http://127.0.0.1:PORT/api/v1 make smoke`.

3. **Web POS** — proxy to the API is already set in `frontend/package.json` (`"proxy": "http://localhost:8080"`):

   ```bash
   cd frontend && npm install && npm start
   ```

   Open the app, log in with **`admin` / `admin123`**.

### First-time database seed

If `backend/data/gbos.db` does not exist yet, the API seeds:

- Store **Main Store**
- Users **`admin`** (owner) / **`cashier`** (cashier) — passwords **`admin123`** / **`cashier123`**
- UOMs, categories, expense categories
- **3 demo products** with prices, stock, and barcodes **`8901001100001`–`8901001100003`**
- Customer **Demo Wholesale Mart** (wholesale + credit limit), vendor **Demo Supplier Co**

To re-seed from scratch, delete `backend/data/gbos.db` and restart the API.

### Environment

See `backend/.env.example`. Variables: `SERVER_PORT`, `DATABASE_PATH`, `JWT_SECRET`.
