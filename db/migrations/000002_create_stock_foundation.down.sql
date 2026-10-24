-- DESTRUCTIVE: removes all stock facts and permanent deduplication. Disposable
-- tests only unless an explicitly reviewed backup/recovery procedure authorizes it.
BEGIN;
DROP TRIGGER stock_balances_integrity ON stock_balances;
DROP TRIGGER stock_movements_integrity ON stock_movements;
DROP TRIGGER stock_operations_integrity ON stock_operations;
DROP TRIGGER stock_idempotency_integrity ON stock_idempotency;
DROP FUNCTION stock_check_deferred();
DROP FUNCTION stock_assert_operation(UUID);
DROP FUNCTION stock_assert_balance(UUID,UUID);
DROP FUNCTION stock_expected_result(UUID);
DROP TRIGGER stock_idempotency_transition ON stock_idempotency;
DROP FUNCTION stock_guard_key_update();
DROP TRIGGER stock_balances_transition ON stock_balances;
DROP FUNCTION stock_guard_balance_update();
DROP TRIGGER stock_operations_immutable ON stock_operations;
DROP TRIGGER stock_movements_immutable ON stock_movements;
DROP TRIGGER stock_balances_no_delete ON stock_balances;
DROP TRIGGER stock_idempotency_no_delete ON stock_idempotency;
DROP FUNCTION stock_reject_mutation();
DROP TABLE stock_movements;
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_key_fk;
DROP TABLE stock_idempotency;
DROP TABLE stock_operations;
DROP TABLE stock_balances;
COMMIT;
