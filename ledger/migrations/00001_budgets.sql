-- +goose Up
CREATE TABLE budgets (
    id SERIAL PRIMARY KEY,
    category TEXT UNIQUE NOT NULL,
    limit_amount NUMERIC(14,2) NOT NULL CHECK (limit_amount > 0)
);

-- +goose Down
DROP TABLE budgets;
