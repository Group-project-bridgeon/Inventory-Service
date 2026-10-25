BEGIN;
-- Bound warehouse-wide and optional-variant traversal without sorting the
-- entire ledger. ID is a total order, not a promise about commit order.
CREATE INDEX stock_movements_warehouse_id_desc ON stock_movements (warehouse_id, id DESC);
CREATE INDEX stock_movements_warehouse_variant_id_desc ON stock_movements (warehouse_id, variant_id, id DESC);
COMMIT;
