BEGIN;

CREATE TABLE stock_balances (
    warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    variant_id UUID NOT NULL,
    base_unit_id UUID NOT NULL,
    on_hand_pieces BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT stock_balances_pkey PRIMARY KEY (warehouse_id, variant_id),
    CONSTRAINT stock_balances_ids_nonzero CHECK (
        warehouse_id <> '00000000-0000-0000-0000-000000000000' AND
        variant_id <> '00000000-0000-0000-0000-000000000000' AND
        base_unit_id <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT stock_balances_nonnegative CHECK (on_hand_pieces >= 0),
    CONSTRAINT stock_balances_version_valid CHECK (version >= 0)
);
CREATE INDEX stock_balances_variant_warehouse ON stock_balances (variant_id, warehouse_id);

CREATE TABLE stock_operations (
    id UUID PRIMARY KEY,
    producer TEXT NOT NULL,
    source_event_id UUID NOT NULL,
    warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
    direction TEXT NOT NULL,
    actor_user_id UUID NOT NULL,
    line_count SMALLINT NOT NULL,
    command_version SMALLINT NOT NULL,
    idempotency_key UUID NOT NULL,
    posted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    first_request_id TEXT,
    CONSTRAINT stock_operations_ids_nonzero CHECK (
        id <> '00000000-0000-0000-0000-000000000000' AND
        source_event_id <> '00000000-0000-0000-0000-000000000000' AND
        actor_user_id <> '00000000-0000-0000-0000-000000000000' AND
        warehouse_id <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT stock_operations_producer_valid CHECK (char_length(btrim(producer)) BETWEEN 1 AND 64),
    CONSTRAINT stock_operations_direction_valid CHECK (direction IN ('IN','OUT')),
    CONSTRAINT stock_operations_line_count_valid CHECK (line_count BETWEEN 1 AND 100),
    CONSTRAINT stock_operations_command_version_valid CHECK (command_version = 1),
    CONSTRAINT stock_operations_request_id_valid CHECK (first_request_id IS NULL OR char_length(first_request_id) <= 64),
    CONSTRAINT stock_operations_source_event_unique UNIQUE (producer, source_event_id),
    CONSTRAINT stock_operations_key_unique UNIQUE (producer, idempotency_key),
    CONSTRAINT stock_operations_id_warehouse_unique UNIQUE (id, warehouse_id)
);

CREATE TABLE stock_idempotency (
    producer TEXT NOT NULL,
    key UUID NOT NULL,
    fingerprint_version SMALLINT NOT NULL,
    request_hash BYTEA NOT NULL,
    actor_user_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    state TEXT NOT NULL DEFAULT 'IN_PROGRESS',
    result_version SMALLINT,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT stock_idempotency_pkey PRIMARY KEY (producer, key),
    CONSTRAINT stock_idempotency_operation_unique UNIQUE (operation_id),
    CONSTRAINT stock_idempotency_operation_fk FOREIGN KEY (operation_id)
        REFERENCES stock_operations(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT stock_idempotency_producer_valid CHECK (char_length(btrim(producer)) BETWEEN 1 AND 64),
    CONSTRAINT stock_idempotency_key_valid CHECK (
        (get_byte(uuid_send(key),6) >> 4) IN (4,7) AND (get_byte(uuid_send(key),8) & 192) = 128),
    CONSTRAINT stock_idempotency_ids_nonzero CHECK (
        actor_user_id <> '00000000-0000-0000-0000-000000000000' AND
        operation_id <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT stock_idempotency_fingerprint_valid CHECK (fingerprint_version = 1 AND octet_length(request_hash) = 32),
    CONSTRAINT stock_idempotency_state_valid CHECK (
        (state = 'IN_PROGRESS' AND result_version IS NULL AND result IS NULL AND completed_at IS NULL) OR
        (state = 'COMPLETED' AND result_version IS NOT NULL AND result_version = 1 AND
         result IS NOT NULL AND jsonb_typeof(result) = 'object' AND completed_at IS NOT NULL))
);
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_key_fk
    FOREIGN KEY (producer, idempotency_key) REFERENCES stock_idempotency(producer, key)
    ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE stock_movements (
    id UUID PRIMARY KEY,
    operation_id UUID NOT NULL,
    line_number SMALLINT NOT NULL,
    warehouse_id UUID NOT NULL,
    variant_id UUID NOT NULL,
    balance_version BIGINT NOT NULL,
    direction TEXT NOT NULL,
    input_count BIGINT NOT NULL,
    pieces_per_input BIGINT NOT NULL,
    pieces BIGINT NOT NULL,
    before_pieces BIGINT NOT NULL,
    after_pieces BIGINT NOT NULL,
    product_id UUID NOT NULL,
    base_unit_id UUID NOT NULL,
    pack_id UUID,
    pack_revision TEXT,
    product_code TEXT NOT NULL,
    product_name TEXT NOT NULL,
    sku TEXT NOT NULL,
    base_unit_code TEXT NOT NULL,
    pack_label TEXT,
    stock_reference_revision TEXT NOT NULL,
    reference_checked_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT stock_movements_operation_fk FOREIGN KEY (operation_id, warehouse_id)
        REFERENCES stock_operations(id, warehouse_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movements_balance_fk FOREIGN KEY (warehouse_id, variant_id)
        REFERENCES stock_balances(warehouse_id, variant_id) ON DELETE RESTRICT,
    CONSTRAINT stock_movements_operation_line_unique UNIQUE (operation_id, line_number),
    CONSTRAINT stock_movements_operation_variant_unique UNIQUE (operation_id, variant_id),
    CONSTRAINT stock_movements_balance_version_unique UNIQUE (warehouse_id, variant_id, balance_version),
    CONSTRAINT stock_movements_ids_valid CHECK (
        (get_byte(uuid_send(id),6) >> 4) = 7 AND (get_byte(uuid_send(id),8) & 192) = 128 AND
        variant_id <> '00000000-0000-0000-0000-000000000000' AND
        product_id <> '00000000-0000-0000-0000-000000000000' AND
        base_unit_id <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT stock_movements_line_valid CHECK (line_number BETWEEN 1 AND 100 AND balance_version > 0),
    CONSTRAINT stock_movements_quantities_positive CHECK (input_count > 0 AND pieces_per_input > 0 AND pieces > 0),
    CONSTRAINT stock_movements_balances_nonnegative CHECK (before_pieces >= 0 AND after_pieces >= 0),
    CONSTRAINT stock_movements_direction_valid CHECK (direction IN ('IN','OUT')),
    -- Widen intermediates only; persisted quantities remain BIGINT integers.
    CONSTRAINT stock_movements_conversion_valid CHECK (input_count::numeric * pieces_per_input::numeric = pieces::numeric),
    CONSTRAINT stock_movements_delta_valid CHECK (after_pieces::numeric =
        CASE direction WHEN 'IN' THEN before_pieces::numeric + pieces::numeric
                       WHEN 'OUT' THEN before_pieces::numeric - pieces::numeric END),
    CONSTRAINT stock_movements_pack_valid CHECK (
        (pack_id IS NULL AND pack_revision IS NULL AND pack_label IS NULL AND pieces_per_input = 1) OR
        (pack_id IS NOT NULL AND pack_id <> '00000000-0000-0000-0000-000000000000' AND
         pack_revision IS NOT NULL AND char_length(btrim(pack_revision)) BETWEEN 1 AND 128)),
    CONSTRAINT stock_movements_snapshots_valid CHECK (
        char_length(btrim(product_code)) BETWEEN 1 AND 50 AND
        char_length(btrim(product_name)) BETWEEN 1 AND 200 AND
        char_length(btrim(sku)) BETWEEN 1 AND 100 AND
        char_length(btrim(base_unit_code)) BETWEEN 1 AND 30 AND
        char_length(btrim(stock_reference_revision)) BETWEEN 1 AND 128)
);

CREATE FUNCTION stock_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'posted stock facts are immutable'
        USING ERRCODE = '23514', CONSTRAINT = 'stock_immutable';
END;
$$;
CREATE TRIGGER stock_operations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stock_operations
    FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER stock_movements_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stock_movements
    FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER stock_balances_no_delete BEFORE DELETE OR TRUNCATE ON stock_balances
    FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER stock_idempotency_no_delete BEFORE DELETE OR TRUNCATE ON stock_idempotency
    FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();

CREATE FUNCTION stock_guard_balance_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.warehouse_id,NEW.variant_id,NEW.base_unit_id,NEW.created_at) IS DISTINCT FROM
       (OLD.warehouse_id,OLD.variant_id,OLD.base_unit_id,OLD.created_at) OR NEW.version <= OLD.version THEN
        RAISE EXCEPTION 'invalid stock balance transition'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_balance_transition';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stock_balances_transition BEFORE UPDATE ON stock_balances
    FOR EACH ROW EXECUTE FUNCTION stock_guard_balance_update();

CREATE FUNCTION stock_guard_key_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.state <> 'IN_PROGRESS' THEN
            RAISE EXCEPTION 'stock keys must be claimed before completion'
                USING ERRCODE = '23514', CONSTRAINT = 'stock_key_transition';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state <> 'IN_PROGRESS' OR NEW.state <> 'COMPLETED' OR
       (NEW.producer,NEW.key,NEW.fingerprint_version,NEW.request_hash,NEW.actor_user_id,NEW.operation_id,NEW.created_at)
       IS DISTINCT FROM
       (OLD.producer,OLD.key,OLD.fingerprint_version,OLD.request_hash,OLD.actor_user_id,OLD.operation_id,OLD.created_at) THEN
        RAISE EXCEPTION 'invalid stock idempotency transition'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_key_transition';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stock_idempotency_transition BEFORE INSERT OR UPDATE ON stock_idempotency
    FOR EACH ROW EXECUTE FUNCTION stock_guard_key_update();

-- Replay v1 is stored once, in stock_idempotency.result. Construct its exact
-- expected value from immutable ledger rows, including decimal-string int64s.
CREATE FUNCTION stock_expected_result(p_operation UUID) RETURNS JSONB
LANGUAGE sql STABLE SET search_path = pg_catalog, public AS $$
    SELECT jsonb_build_object(
        'operation_id',o.id::text, 'result_version','1',
        'posted_at',to_char(o.posted_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        'lines',COALESCE((SELECT jsonb_agg(jsonb_build_object(
            'line_number',m.line_number,'variant_id',m.variant_id::text,'movement_id',m.id::text,
            'after_pieces',m.after_pieces::text,'balance_version',m.balance_version::text)
            ORDER BY m.line_number) FROM stock_movements m WHERE m.operation_id=o.id),'[]'::jsonb))
    FROM stock_operations o WHERE o.id=p_operation;
$$;

CREATE FUNCTION stock_assert_balance(p_warehouse UUID,p_variant UUID) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
DECLARE b stock_balances%ROWTYPE; m stock_movements%ROWTYPE;
BEGIN
    SELECT * INTO b FROM stock_balances WHERE warehouse_id=p_warehouse AND variant_id=p_variant;
    SELECT * INTO m FROM stock_movements WHERE warehouse_id=p_warehouse AND variant_id=p_variant
        ORDER BY balance_version DESC LIMIT 1;
    IF b.version IS NULL OR m.id IS NULL OR b.version <> m.balance_version OR
       b.on_hand_pieces <> m.after_pieces OR b.base_unit_id <> m.base_unit_id THEN
        RAISE EXCEPTION 'stock balance ledger mismatch'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_balance_ledger_integrity';
    END IF;
END;
$$;

CREATE FUNCTION stock_assert_operation(p_operation UUID) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
DECLARE o stock_operations%ROWTYPE; k stock_idempotency%ROWTYPE;
BEGIN
    SELECT * INTO o FROM stock_operations WHERE id=p_operation;
    SELECT * INTO k FROM stock_idempotency WHERE operation_id=p_operation;
    IF o.id IS NULL OR k.operation_id IS NULL OR k.state <> 'COMPLETED' OR
       (o.producer,o.idempotency_key,o.actor_user_id) IS DISTINCT FROM (k.producer,k.key,k.actor_user_id) OR
       (SELECT count(*) FROM stock_movements WHERE operation_id=p_operation) <> o.line_count OR
       EXISTS (SELECT 1 FROM stock_movements m JOIN stock_balances b
               ON (b.warehouse_id,b.variant_id)=(m.warehouse_id,m.variant_id)
               LEFT JOIN stock_movements prev ON (prev.warehouse_id,prev.variant_id,prev.balance_version)=
                   (m.warehouse_id,m.variant_id,m.balance_version-1)
               WHERE m.operation_id=p_operation AND
                   (m.line_number > o.line_count OR m.direction <> o.direction OR m.base_unit_id <> b.base_unit_id OR
                    (m.balance_version=1 AND m.before_pieces<>0) OR
                    (m.balance_version>1 AND (prev.id IS NULL OR prev.after_pieces<>m.before_pieces)))) OR
       k.result IS DISTINCT FROM stock_expected_result(p_operation) THEN
        RAISE EXCEPTION 'stock operation correspondence mismatch'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_operation_integrity';
    END IF;
END;
$$;

CREATE FUNCTION stock_check_deferred() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    IF TG_TABLE_NAME='stock_balances' THEN
        PERFORM stock_assert_balance(NEW.warehouse_id,NEW.variant_id);
    ELSIF TG_TABLE_NAME='stock_movements' THEN
        PERFORM stock_assert_balance(NEW.warehouse_id,NEW.variant_id);
        PERFORM stock_assert_operation(NEW.operation_id);
    ELSIF TG_TABLE_NAME='stock_operations' THEN
        PERFORM stock_assert_operation(NEW.id);
    ELSE
        PERFORM stock_assert_operation(NEW.operation_id);
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER stock_balances_integrity AFTER INSERT OR UPDATE ON stock_balances
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_deferred();
CREATE CONSTRAINT TRIGGER stock_movements_integrity AFTER INSERT ON stock_movements
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_deferred();
CREATE CONSTRAINT TRIGGER stock_operations_integrity AFTER INSERT ON stock_operations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_deferred();
CREATE CONSTRAINT TRIGGER stock_idempotency_integrity AFTER INSERT OR UPDATE ON stock_idempotency
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_deferred();

COMMIT;
