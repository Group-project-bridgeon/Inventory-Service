CREATE TABLE warehouses (
    id UUID PRIMARY KEY,
    code TEXT NOT NULL CONSTRAINT warehouses_code_unique UNIQUE,
    name TEXT NOT NULL,
    address TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT true,
    is_default BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT warehouses_code_format CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,49}$'),
    CONSTRAINT warehouses_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT warehouses_address_valid CHECK (char_length(address) <= 500),
    CONSTRAINT warehouses_default_active CHECK (NOT is_default OR is_active)
);
CREATE UNIQUE INDEX warehouses_one_default ON warehouses (is_default) WHERE is_default;

-- The partial index prevents two defaults; this deferred trigger also requires
-- one default at commit whenever any warehouse rows exist.
CREATE FUNCTION enforce_one_active_default_warehouse() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM warehouses)
       AND NOT EXISTS (SELECT 1 FROM warehouses WHERE is_default AND is_active) THEN
        RAISE EXCEPTION 'a warehouse must remain the active default'
            USING ERRCODE = '23514', CONSTRAINT = 'warehouses_exactly_one_default';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER warehouses_exactly_one_default
AFTER INSERT OR UPDATE OR DELETE ON warehouses
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_one_active_default_warehouse();
