package postgres

import (
	"os"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/google/uuid"
)

// Run before migration 6 using raw, explicitly old-column SQL. The same binary's
// generated queries target the new schema and are deliberately not used to seed.
func TestReservationLegacySeed(t *testing.T) {
	if os.Getenv("RESERVATION_TEST_PHASE") != "seed" {
		t.Skip("requires owned pre-migration fixture phase")
	}
	f := postingDB(t)
	c := f.command(stock.In, 100)
	fp, err := c.Fingerprint(f.p)
	postingOK(t, err)
	tx, err := f.pool.Begin(f.ctx)
	postingOK(t, err)
	defer tx.Rollback(f.ctx)
	_, err = tx.Exec(f.ctx, `INSERT INTO stock_balances(warehouse_id,variant_id,base_unit_id,on_hand_pieces,version) VALUES($1,$2,$3,100,1)`, f.warehouse, f.variant, f.unit)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `INSERT INTO stock_idempotency(producer,key,fingerprint_version,request_hash,actor_user_id,operation_id) VALUES($1,$2,1,$3,$4,$5)`, c.Producer, c.IdempotencyKey, fp.Hash[:], f.p.UserID, c.OperationID)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `INSERT INTO stock_operations(id,producer,source_event_id,warehouse_id,direction,actor_user_id,line_count,command_version,idempotency_key) VALUES($1,$2,$3,$4,'IN',$5,1,1,$6)`, c.OperationID, c.Producer, c.SourceEventID, c.WarehouseID, f.p.UserID, c.IdempotencyKey)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `INSERT INTO stock_movements(id,operation_id,line_number,warehouse_id,variant_id,balance_version,direction,input_count,pieces_per_input,pieces,before_pieces,after_pieces,product_id,base_unit_id,product_code,product_name,sku,base_unit_code,stock_reference_revision,reference_checked_at) VALUES($1,$2,1,$3,$4,1,'IN',100,1,100,0,100,$4,$5,'LEGACY','Legacy fixture','LEGACY-SKU','PC','synthetic-v1',now())`, uuid.Must(uuid.NewV7()), c.OperationID, c.WarehouseID, f.variant, f.unit)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `UPDATE stock_idempotency SET state='COMPLETED',result_version=1,result=stock_expected_result(operation_id),completed_at=now() WHERE producer=$1 AND key=$2`, c.Producer, c.IdempotencyKey)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `CREATE TABLE fixture_reservation_legacy(operation_id uuid,source_id uuid,warehouse_id uuid,variant_id uuid,unit_id uuid,actor_id uuid,key_id uuid)`)
	postingOK(t, err)
	_, err = tx.Exec(f.ctx, `INSERT INTO fixture_reservation_legacy VALUES($1,$2,$3,$4,$5,$6,$7)`, c.OperationID, c.SourceEventID, c.WarehouseID, f.variant, f.unit, f.p.UserID, c.IdempotencyKey)
	postingOK(t, err)
	postingOK(t, tx.Commit(f.ctx))
	t.Log("PASS: pre-migration human v1 operation/result persisted without new actor or reservation columns")
}
func TestReservationLegacyReplay(t *testing.T) {
	if os.Getenv("RESERVATION_TEST_PHASE") != "verify" {
		t.Skip("requires owned post-migration fixture phase")
	}
	f := postingDB(t)
	c := f.command(stock.In, 100)
	var actor, variant, unit uuid.UUID
	postingOK(t, f.pool.QueryRow(f.ctx, `SELECT operation_id,source_id,warehouse_id,variant_id,unit_id,actor_id,key_id FROM fixture_reservation_legacy`).Scan(&c.OperationID, &c.SourceEventID, &c.WarehouseID, &variant, &unit, &actor, &c.IdempotencyKey))
	c.Lines[0].VariantID = variant
	principal := stock.Principal{UserID: actor}
	fp, err := c.Fingerprint(principal)
	postingOK(t, err)
	result, found, err := f.repo.Lookup(f.ctx, principal, c, fp)
	postingOK(t, err)
	if !found || !result.Replayed || result.ResultVersion != 1 || result.Lines[0].AfterPieces != 100 {
		t.Fatal("legacy replay changed")
	}
	t.Log("PASS: migration preserved v1 fingerprint, HUMAN identity, result v1 and balance")
}
