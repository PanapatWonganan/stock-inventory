CREATE TABLE IF NOT EXISTS users (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username text NOT NULL UNIQUE,
  password_hash bytea NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash bytea PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS products (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  sku text NOT NULL UNIQUE,
  name text NOT NULL,
  unit text NOT NULL
);
CREATE TABLE IF NOT EXISTS warehouses (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code text NOT NULL UNIQUE,
  name text NOT NULL
);
CREATE TABLE IF NOT EXISTS stock_balances (
  product_id bigint REFERENCES products(id),
  warehouse_id bigint REFERENCES warehouses(id),
  quantity bigint NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  PRIMARY KEY (product_id, warehouse_id)
);
CREATE TABLE IF NOT EXISTS inventory_transactions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id),
  request_key text NOT NULL,
  request_hash bytea NOT NULL,
  kind text NOT NULL CHECK (kind IN ('receive','issue','transfer','adjust')),
  reason text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, request_key)
);
CREATE TABLE IF NOT EXISTS inventory_movements (
  transaction_id bigint REFERENCES inventory_transactions(id),
  product_id bigint REFERENCES products(id),
  warehouse_id bigint REFERENCES warehouses(id),
  delta bigint NOT NULL,
  PRIMARY KEY (transaction_id, product_id, warehouse_id)
);
CREATE INDEX IF NOT EXISTS inventory_movements_lookup ON inventory_movements(product_id,warehouse_id,transaction_id);
