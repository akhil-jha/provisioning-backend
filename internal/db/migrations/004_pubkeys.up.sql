BEGIN;

CREATE TABLE pubkeys
(
  id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  account_id BIGINT NOT NULL REFERENCES accounts(id),
  name TEXT NOT NULL CHECK (NOT empty(name)),
  body TEXT NOT NULL CHECK (NOT empty(body))
);

COMMIT;