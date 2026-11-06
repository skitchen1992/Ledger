-- +goose Up
-- Gateway accepts finite positive float64 values without cent rounding and
-- preserves optional/free-form date strings. Existing SQL dates remain ISO dates.
ALTER TABLE budgets ALTER COLUMN limit_amount TYPE NUMERIC;
ALTER TABLE expenses ALTER COLUMN amount TYPE NUMERIC;
ALTER TABLE expenses ALTER COLUMN date TYPE TEXT USING to_char(date, 'YYYY-MM-DD');
-- Escape existing backslashes before repositories begin escaping NUL as \0.
-- TYPE USING rewrites all keys before rebuilding the unique index, so categories
-- containing one and two backslashes cannot collide during the conversion.
ALTER TABLE budgets ALTER COLUMN category TYPE TEXT
    USING replace(category, chr(92), chr(92) || chr(92));
ALTER TABLE expenses
    ALTER COLUMN category TYPE TEXT USING replace(category, chr(92), chr(92) || chr(92)),
    ALTER COLUMN description TYPE TEXT USING replace(description, chr(92), chr(92) || chr(92));

-- +goose Down
-- Check every conversion before changing the schema so a rollback cannot silently
-- round money or normalize/replace Gateway's free-form dates.
LOCK TABLE budgets, expenses IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM budgets
        WHERE strpos(replace(category, chr(92) || chr(92), ''), chr(92) || '0') > 0
    ) OR EXISTS (
        SELECT 1 FROM expenses
        WHERE strpos(replace(category, chr(92) || chr(92), ''), chr(92) || '0') > 0
            OR strpos(replace(description, chr(92) || chr(92), ''), chr(92) || '0') > 0
            OR strpos(replace(date, chr(92) || chr(92), ''), chr(92) || '0') > 0
    ) THEN
        RAISE EXCEPTION 'cannot roll back Gateway compatibility: text contains NUL';
    END IF;

    IF EXISTS (
        SELECT 1 FROM budgets
        WHERE strpos(replace(category, chr(92) || chr(92), ''), chr(92)) > 0
    ) OR EXISTS (
        SELECT 1 FROM expenses
        WHERE strpos(replace(category, chr(92) || chr(92), ''), chr(92)) > 0
            OR strpos(replace(description, chr(92) || chr(92), ''), chr(92)) > 0
            OR strpos(replace(date, chr(92) || chr(92), ''), chr(92)) > 0
    ) THEN
        RAISE EXCEPTION 'cannot roll back Gateway compatibility: text contains an invalid escape';
    END IF;

    IF EXISTS (
        SELECT 1 FROM budgets
        WHERE limit_amount <> round(limit_amount, 2)
            OR abs(limit_amount) > 999999999999.99
    ) OR EXISTS (
        SELECT 1 FROM expenses
        WHERE amount <> round(amount, 2)
            OR abs(amount) > 999999999999.99
    ) THEN
        RAISE EXCEPTION 'cannot roll back Gateway compatibility: money is not representable as NUMERIC(14,2)';
    END IF;

    IF EXISTS (
        SELECT 1 FROM expenses
        WHERE NOT (
            date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
            AND pg_input_is_valid(date, 'date')
        )
    ) THEN
        RAISE EXCEPTION 'cannot roll back Gateway compatibility: an expense date is not canonical YYYY-MM-DD';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE budgets ALTER COLUMN limit_amount TYPE NUMERIC(14,2);
ALTER TABLE expenses ALTER COLUMN amount TYPE NUMERIC(14,2);
ALTER TABLE expenses ALTER COLUMN date TYPE DATE USING date::date;
ALTER TABLE budgets ALTER COLUMN category TYPE TEXT
    USING replace(category, chr(92) || chr(92), chr(92));
ALTER TABLE expenses
    ALTER COLUMN category TYPE TEXT USING replace(category, chr(92) || chr(92), chr(92)),
    ALTER COLUMN description TYPE TEXT USING replace(description, chr(92) || chr(92), chr(92));
