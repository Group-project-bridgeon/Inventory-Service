BEGIN;
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_command_version_valid;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_command_version_valid CHECK(command_version IN (1,2));
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_idempotency_fingerprint_valid;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_idempotency_fingerprint_valid CHECK(fingerprint_version IN (1,2) AND octet_length(request_hash)=32);

CREATE TABLE stock_operation_audit (
 operation_id UUID PRIMARY KEY REFERENCES stock_operations(id) ON DELETE RESTRICT,
 reason_code TEXT NOT NULL CHECK(reason_code IN ('DAMAGE','LOSS','FOUND','DATA_CORRECTION','OTHER')),
 explanation TEXT NOT NULL CHECK(char_length(explanation) BETWEEN 1 AND 500 AND explanation=btrim(explanation) AND explanation !~ '[[:cntrl:]]'),
 original_operation_id UUID REFERENCES stock_operations(id) ON DELETE RESTRICT,
 CONSTRAINT stock_audit_original_unique UNIQUE(original_operation_id),
 CHECK(original_operation_id IS NULL OR original_operation_id<>operation_id)
);
CREATE TRIGGER stock_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stock_operation_audit
 FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();

-- Separate from the existing operation correspondence/Opening guard: preserve
-- the frozen result v1 and all earlier integrity checks.
CREATE FUNCTION stock_assert_adjustment(p_id UUID) RETURNS void
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
CREATE FUNCTION stock_check_adjustment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='stock_operations' THEN PERFORM stock_assert_adjustment(NEW.id);
 ELSE PERFORM stock_assert_adjustment(NEW.operation_id); END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER stock_operation_adjustment_check AFTER INSERT ON stock_operations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_adjustment();
CREATE CONSTRAINT TRIGGER stock_audit_check AFTER INSERT ON stock_operation_audit
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION stock_check_adjustment();
COMMIT;
