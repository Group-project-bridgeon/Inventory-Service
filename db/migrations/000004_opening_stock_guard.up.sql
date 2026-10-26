BEGIN;
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
COMMIT;
