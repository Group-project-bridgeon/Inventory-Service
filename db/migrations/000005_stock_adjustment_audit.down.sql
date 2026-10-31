BEGIN;
-- Downgrade is safe only before v2 history exists. Never erase posted audit.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM stock_operation_audit) OR EXISTS(SELECT 1 FROM stock_operations WHERE command_version=2) THEN
  RAISE EXCEPTION 'stock adjustment history requires forward recovery';
 END IF;
END $$;
DROP TRIGGER stock_operation_adjustment_check ON stock_operations;
DROP TABLE stock_operation_audit;
DROP FUNCTION stock_check_adjustment();
DROP FUNCTION stock_assert_adjustment(UUID);
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_command_version_valid;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_command_version_valid CHECK(command_version=1);
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_idempotency_fingerprint_valid;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_idempotency_fingerprint_valid CHECK(fingerprint_version=1 AND octet_length(request_hash)=32);
COMMIT;
