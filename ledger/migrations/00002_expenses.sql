-- +goose Up
CREATE TABLE expenses (
    id SERIAL PRIMARY KEY,
    amount NUMERIC(14,2) NOT NULL CHECK (amount <> 0),
    category TEXT NOT NULL,
    description TEXT,
    date DATE NOT NULL
);

CREATE INDEX expenses_category_date_idx ON expenses (category, date);

-- +goose Down
DROP TABLE expenses;
