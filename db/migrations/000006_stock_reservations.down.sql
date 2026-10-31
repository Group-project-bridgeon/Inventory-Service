BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM stock_reservations) OR EXISTS(SELECT 1 FROM stock_operations WHERE actor_kind='MACHINE') OR EXISTS(SELECT 1 FROM stock_balances WHERE reserved_pieces<>0 OR reservation_version<>0) THEN
  RAISE EXCEPTION 'reservation history requires reviewed forward recovery';
 END IF;
END $$;
DROP TRIGGER reservation_balance_check ON stock_balances;

CREATE OR REPLACE FUNCTION stock_assert_operation(p_operation UUID) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
DECLARE o stock_operations%ROWTYPE; k stock_idempotency%ROWTYPE;
BEGIN
    SELECT * INTO o FROM stock_operations WHERE id=p_operation;
    SELECT * INTO k FROM stock_idempotency WHERE operation_id=p_operation;
    IF o.producer='inventory.opening-stock.v1' AND (o.direction<>'IN' OR o.source_event_id<>o.id OR EXISTS (SELECT 1 FROM stock_movements WHERE operation_id=o.id AND (before_pieces<>0 OR balance_version<>1))) THEN
        RAISE EXCEPTION 'invalid opening stock history' USING ERRCODE='23514',CONSTRAINT='stock_opening_integrity';
    END IF;
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
CREATE OR REPLACE FUNCTION stock_assert_adjustment(p_id UUID) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE o stock_operations%ROWTYPE; a stock_operation_audit%ROWTYPE; original stock_operations%ROWTYPE; fp SMALLINT;
BEGIN
 SELECT * INTO o FROM stock_operations WHERE id=p_id;
 SELECT * INTO a FROM stock_operation_audit WHERE operation_id=p_id;
 SELECT fingerprint_version INTO fp FROM stock_idempotency WHERE operation_id=p_id;
 IF o.command_version IS DISTINCT FROM fp THEN
  RAISE EXCEPTION 'invalid stock intent version' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
 END IF;
 IF o.producer NOT IN ('inventory.stock-adjustments.v1','inventory.stock-corrections.v1') THEN
  IF o.command_version<>1 OR a.operation_id IS NOT NULL THEN
   RAISE EXCEPTION 'invalid legacy stock metadata' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
  END IF;
  RETURN;
 END IF;
 IF o.command_version<>2 OR a.operation_id IS NULL OR o.source_event_id<>o.id THEN
  RAISE EXCEPTION 'missing stock audit' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
 END IF;
 IF o.producer='inventory.stock-adjustments.v1' THEN
  IF a.original_operation_id IS NOT NULL THEN
   RAISE EXCEPTION 'invalid adjustment target' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
  END IF;
  RETURN;
 END IF;
 SELECT * INTO original FROM stock_operations WHERE id=a.original_operation_id;
 IF original.id IS NULL OR original.producer NOT IN ('inventory.opening-stock.v1','inventory.stock-adjustments.v1')
    OR original.warehouse_id<>o.warehouse_id OR original.direction=o.direction OR original.line_count<>o.line_count
    OR EXISTS (
     SELECT 1 FROM stock_movements m LEFT JOIN stock_movements old
      ON old.operation_id=original.id AND old.line_number=m.line_number
     WHERE m.operation_id=o.id AND (old.id IS NULL OR
      (m.variant_id,m.product_id,m.base_unit_id,m.input_count,m.pieces_per_input,m.pieces,m.pack_id,m.pack_revision,
       m.product_code,m.product_name,m.sku,m.base_unit_code,m.pack_label) IS DISTINCT FROM
      (old.variant_id,old.product_id,old.base_unit_id,old.input_count,old.pieces_per_input,old.pieces,old.pack_id,old.pack_revision,
       old.product_code,old.product_name,old.sku,old.base_unit_code,old.pack_label))) THEN
  RAISE EXCEPTION 'invalid whole stock reversal' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
 END IF;
END;
$$;
CREATE OR REPLACE FUNCTION stock_guard_balance_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.warehouse_id,NEW.variant_id,NEW.base_unit_id,NEW.created_at) IS DISTINCT FROM
       (OLD.warehouse_id,OLD.variant_id,OLD.base_unit_id,OLD.created_at) OR NEW.version <= OLD.version THEN
        RAISE EXCEPTION 'invalid stock balance transition'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_balance_transition';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION stock_guard_key_update() RETURNS trigger LANGUAGE plpgsql AS $$
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

DROP FUNCTION stock_assert_operation_before_reservations(UUID);
DROP FUNCTION stock_assert_adjustment_before_reservations(UUID);
ALTER TABLE reservation_orders DROP CONSTRAINT reservation_order_current_fk;
DROP TABLE reservation_transitions;
DROP TABLE reservation_commands;
DROP TABLE reservation_lines;
DROP TABLE stock_reservations;
DROP TABLE reservation_orders;
DROP FUNCTION reservation_check_deferred();
DROP FUNCTION reservation_guard_projection();
DROP FUNCTION reservation_guard_command();
DROP FUNCTION reservation_assert_header(UUID);
DROP FUNCTION reservation_assert_order(UUID,UUID);
DROP FUNCTION reservation_assert_balance(UUID,UUID);
DROP FUNCTION reservation_assert_command(UUID,UUID);
DROP FUNCTION reservation_assert_consume(UUID);
DROP FUNCTION reservation_expected_result(UUID,UUID);
ALTER TABLE stock_operations DROP CONSTRAINT stock_operation_actor_valid;
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_key_actor_valid;
ALTER TABLE stock_operations ALTER COLUMN actor_user_id SET NOT NULL;
ALTER TABLE stock_idempotency ALTER COLUMN actor_user_id SET NOT NULL;
ALTER TABLE stock_operations DROP COLUMN actor_kind,DROP COLUMN actor_machine_id;
ALTER TABLE stock_idempotency DROP COLUMN actor_kind,DROP COLUMN actor_machine_id;
ALTER TABLE stock_balances DROP CONSTRAINT stock_available_valid;
ALTER TABLE stock_balances DROP COLUMN reserved_pieces,DROP COLUMN reservation_version;
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_command_version_valid;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_command_version_valid CHECK(command_version IN(1,2));
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_idempotency_fingerprint_valid;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_idempotency_fingerprint_valid CHECK(fingerprint_version IN(1,2) AND octet_length(request_hash)=32);
COMMIT;
