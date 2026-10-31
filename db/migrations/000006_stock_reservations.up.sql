BEGIN;
ALTER TABLE stock_balances ADD COLUMN reserved_pieces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE stock_balances ADD COLUMN reservation_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE stock_balances ADD CONSTRAINT stock_available_valid CHECK(reserved_pieces>=0 AND reserved_pieces<=on_hand_pieces AND reservation_version>=0);
ALTER TABLE stock_operations ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'HUMAN';
ALTER TABLE stock_operations ADD COLUMN actor_machine_id UUID;
ALTER TABLE stock_operations ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operation_actor_valid CHECK(
 (actor_kind='HUMAN' AND actor_user_id IS NOT NULL AND actor_machine_id IS NULL) OR
 (actor_kind='MACHINE' AND actor_user_id IS NULL AND actor_machine_id IS NOT NULL AND actor_machine_id<>'00000000-0000-0000-0000-000000000000'));
ALTER TABLE stock_idempotency ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'HUMAN';
ALTER TABLE stock_idempotency ADD COLUMN actor_machine_id UUID;
ALTER TABLE stock_idempotency ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_key_actor_valid CHECK(
 (actor_kind='HUMAN' AND actor_user_id IS NOT NULL AND actor_machine_id IS NULL) OR
 (actor_kind='MACHINE' AND actor_user_id IS NULL AND actor_machine_id IS NOT NULL AND actor_machine_id<>'00000000-0000-0000-0000-000000000000'));
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_command_version_valid;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_command_version_valid CHECK(command_version IN(1,2,3));
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_idempotency_fingerprint_valid;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_idempotency_fingerprint_valid CHECK(fingerprint_version IN(1,2,3) AND octet_length(request_hash)=32);

CREATE TABLE reservation_orders (
 owner_machine_id UUID NOT NULL CHECK(owner_machine_id<>'00000000-0000-0000-0000-000000000000'),
 order_id UUID NOT NULL CHECK(order_id<>'00000000-0000-0000-0000-000000000000'),
 generation BIGINT NOT NULL DEFAULT 0 CHECK(generation>=0),
 version BIGINT NOT NULL DEFAULT 0 CHECK(version>=0),
 current_reservation_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(owner_machine_id,order_id)
);
CREATE TABLE stock_reservations (
 id UUID PRIMARY KEY CHECK(id<>'00000000-0000-0000-0000-000000000000'),
 owner_machine_id UUID NOT NULL,
 order_id UUID NOT NULL,
 warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
 generation BIGINT NOT NULL CHECK(generation>0),
 state TEXT NOT NULL CHECK(state IN('ACTIVE','RELEASED','CONSUMED')),
 version BIGINT NOT NULL CHECK(version IN(1,2)),
 order_version BIGINT NOT NULL CHECK(order_version>0),
 line_count SMALLINT NOT NULL CHECK(line_count BETWEEN 1 AND 100),
 operation_id UUID REFERENCES stock_operations(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CONSTRAINT reservation_business_unique UNIQUE(owner_machine_id,order_id,generation),
 UNIQUE(id,owner_machine_id,order_id),
 FOREIGN KEY(owner_machine_id,order_id) REFERENCES reservation_orders(owner_machine_id,order_id) ON DELETE RESTRICT,
 CHECK((state='ACTIVE' AND version=1 AND operation_id IS NULL) OR(state='RELEASED' AND version=2 AND operation_id IS NULL) OR(state='CONSUMED' AND version=2 AND operation_id IS NOT NULL))
);
ALTER TABLE reservation_orders ADD CONSTRAINT reservation_order_current_fk FOREIGN KEY(current_reservation_id) REFERENCES stock_reservations(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX reservations_owner_active ON stock_reservations(owner_machine_id,id) WHERE state='ACTIVE';
CREATE TABLE reservation_lines (
 reservation_id UUID NOT NULL REFERENCES stock_reservations(id) ON DELETE RESTRICT,
 line_number SMALLINT NOT NULL CHECK(line_number BETWEEN 1 AND 100),
 order_line_id UUID NOT NULL CHECK(order_line_id<>'00000000-0000-0000-0000-000000000000'),
 variant_id UUID NOT NULL CHECK(variant_id<>'00000000-0000-0000-0000-000000000000'),
 pieces BIGINT NOT NULL CHECK(pieces>0),
 product_id UUID NOT NULL CHECK(product_id<>'00000000-0000-0000-0000-000000000000'),
 base_unit_id UUID NOT NULL CHECK(base_unit_id<>'00000000-0000-0000-0000-000000000000'),
 product_code TEXT NOT NULL CHECK(char_length(btrim(product_code)) BETWEEN 1 AND 50),
 product_name TEXT NOT NULL CHECK(char_length(btrim(product_name)) BETWEEN 1 AND 200),
 sku TEXT NOT NULL CHECK(char_length(btrim(sku)) BETWEEN 1 AND 100),
 base_unit_code TEXT NOT NULL CHECK(char_length(btrim(base_unit_code)) BETWEEN 1 AND 30),
 stock_reference_revision TEXT NOT NULL CHECK(char_length(btrim(stock_reference_revision)) BETWEEN 1 AND 128),
 reference_checked_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(reservation_id,line_number),
 UNIQUE(reservation_id,order_line_id), UNIQUE(reservation_id,variant_id)
);
CREATE INDEX reservation_lines_variant ON reservation_lines(variant_id,reservation_id);
CREATE TABLE reservation_commands (
 owner_machine_id UUID NOT NULL CHECK(owner_machine_id<>'00000000-0000-0000-0000-000000000000'),
 key UUID NOT NULL CHECK((get_byte(uuid_send(key),6)>>4) IN(4,7) AND (get_byte(uuid_send(key),8)&192)=128),
 command_id UUID NOT NULL CHECK((get_byte(uuid_send(command_id),6)>>4) IN(4,7) AND (get_byte(uuid_send(command_id),8)&192)=128),
 reservation_id UUID NOT NULL REFERENCES stock_reservations(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
 order_id UUID NOT NULL,
 warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
 generation BIGINT NOT NULL CHECK(generation>0),
 action TEXT NOT NULL CHECK(action IN('RESERVE','RELEASE','CONSUME')),
 fingerprint_version SMALLINT NOT NULL CHECK(fingerprint_version=1),
 request_hash BYTEA NOT NULL CHECK(octet_length(request_hash)=32),
 state TEXT NOT NULL DEFAULT 'IN_PROGRESS',
 result_version SMALLINT,
 result JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ,
 PRIMARY KEY(owner_machine_id,key),
 CONSTRAINT reservation_command_identity_unique UNIQUE(owner_machine_id,command_id),
 CHECK((state='IN_PROGRESS' AND result_version IS NULL AND result IS NULL AND completed_at IS NULL) OR
 (state='COMPLETED' AND result_version=1 AND result_version IS NOT NULL AND result IS NOT NULL AND jsonb_typeof(result)='object' AND completed_at IS NOT NULL))
);
CREATE TABLE reservation_transitions (
 id UUID PRIMARY KEY CHECK((get_byte(uuid_send(id),6)>>4)=7 AND (get_byte(uuid_send(id),8)&192)=128),
 reservation_id UUID NOT NULL,
 owner_machine_id UUID NOT NULL,
 order_id UUID NOT NULL,
 command_id UUID NOT NULL,
 action TEXT NOT NULL,
 state TEXT NOT NULL,
 reservation_version BIGINT NOT NULL,
 order_version BIGINT NOT NULL CHECK(order_version>0),
 operation_id UUID REFERENCES stock_operations(id) ON DELETE RESTRICT,
 first_request_id TEXT CHECK(first_request_id IS NULL OR char_length(first_request_id)<=64),
 changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(reservation_id,owner_machine_id,order_id) REFERENCES stock_reservations(id,owner_machine_id,order_id) ON DELETE RESTRICT,
 FOREIGN KEY(owner_machine_id,command_id) REFERENCES reservation_commands(owner_machine_id,command_id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
 UNIQUE(reservation_id,reservation_version), UNIQUE(reservation_id,action), UNIQUE(owner_machine_id,command_id), UNIQUE(owner_machine_id,order_id,order_version),
 CHECK((action='RESERVE' AND state='ACTIVE' AND reservation_version=1 AND operation_id IS NULL) OR
 (action='RELEASE' AND state='RELEASED' AND reservation_version=2 AND operation_id IS NULL) OR
 (action='CONSUME' AND state='CONSUMED' AND reservation_version=2 AND operation_id IS NOT NULL))
);
CREATE FUNCTION reservation_expected_result(p_owner UUID,p_key UUID) RETURNS JSONB LANGUAGE sql STABLE SET search_path=pg_catalog,public AS $$
 SELECT jsonb_build_object('result_version','1','reservation_id',r.id::text,'order_id',r.order_id::text,'warehouse_id',r.warehouse_id::text,
 'generation',r.generation::text,'state',t.state,'version',t.reservation_version::text,'order_version',t.order_version::text,
 'changed_at',to_char(t.changed_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
 'operation_id',t.operation_id::text,'stock_result',CASE WHEN t.operation_id IS NULL THEN NULL ELSE stock_expected_result(t.operation_id) END)
 FROM reservation_commands c JOIN reservation_transitions t ON(t.owner_machine_id,t.command_id)=(c.owner_machine_id,c.command_id)
 JOIN stock_reservations r ON r.id=t.reservation_id WHERE c.owner_machine_id=p_owner AND c.key=p_key;
$$;
CREATE FUNCTION reservation_assert_consume(p_operation UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE o stock_operations%ROWTYPE; r stock_reservations%ROWTYPE; t reservation_transitions%ROWTYPE;
BEGIN
 SELECT * INTO o FROM stock_operations WHERE id=p_operation;
 SELECT * INTO t FROM reservation_transitions WHERE operation_id=p_operation;
 SELECT * INTO r FROM stock_reservations WHERE id=t.reservation_id;
 IF o.id IS NULL OR t.id IS NULL OR r.id IS NULL OR t.action<>'CONSUME' OR r.state<>'CONSUMED' OR r.operation_id<>o.id OR
 (o.producer,o.direction,o.command_version,o.actor_kind,o.actor_machine_id,o.source_event_id,o.id,o.warehouse_id,o.line_count) IS DISTINCT FROM
 ('inventory.reservations.consume.v1','OUT',3::smallint,'MACHINE',r.owner_machine_id,r.id,t.command_id,r.warehouse_id,r.line_count) OR
 EXISTS(SELECT 1 FROM stock_movements m LEFT JOIN reservation_lines l ON l.reservation_id=r.id AND l.line_number=m.line_number
 WHERE m.operation_id=o.id AND (l.reservation_id IS NULL OR
 (m.variant_id,m.product_id,m.base_unit_id,m.input_count,m.pieces_per_input,m.pieces,m.pack_id) IS DISTINCT FROM
 (l.variant_id,l.product_id,l.base_unit_id,l.pieces,1::bigint,l.pieces,NULL::uuid))) THEN
  RAISE EXCEPTION 'reservation consumption correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_consume_integrity';
 END IF;
END;
$$;
CREATE FUNCTION reservation_assert_command(p_owner UUID,p_key UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE c reservation_commands%ROWTYPE; t reservation_transitions%ROWTYPE; r stock_reservations%ROWTYPE;
BEGIN
 SELECT * INTO c FROM reservation_commands WHERE owner_machine_id=p_owner AND key=p_key;
 SELECT * INTO t FROM reservation_transitions WHERE owner_machine_id=p_owner AND command_id=c.command_id;
 SELECT * INTO r FROM stock_reservations WHERE id=t.reservation_id;
 IF c.command_id IS NULL OR t.id IS NULL OR r.id IS NULL OR c.state<>'COMPLETED' OR
 (c.reservation_id,c.owner_machine_id,c.order_id,c.warehouse_id,c.generation,c.action) IS DISTINCT FROM
 (r.id,r.owner_machine_id,r.order_id,r.warehouse_id,r.generation,t.action) OR
 c.result IS DISTINCT FROM reservation_expected_result(p_owner,p_key) THEN
  RAISE EXCEPTION 'reservation command correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_command_integrity';
 END IF;
 IF t.action='CONSUME' THEN PERFORM reservation_assert_consume(t.operation_id); END IF;
END;
$$;
CREATE FUNCTION reservation_assert_balance(p_wh UUID,p_variant UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE b stock_balances%ROWTYPE; held NUMERIC; revision BIGINT;
BEGIN
 SELECT * INTO b FROM stock_balances WHERE warehouse_id=p_wh AND variant_id=p_variant;
 SELECT COALESCE(sum(l.pieces::numeric),0) INTO held FROM reservation_lines l JOIN stock_reservations r ON r.id=l.reservation_id
 WHERE r.warehouse_id=p_wh AND l.variant_id=p_variant AND r.state='ACTIVE';
 SELECT count(*) INTO revision FROM reservation_transitions t JOIN stock_reservations r ON r.id=t.reservation_id
 JOIN reservation_lines l ON l.reservation_id=r.id WHERE r.warehouse_id=p_wh AND l.variant_id=p_variant;
 IF b.warehouse_id IS NULL OR b.reserved_pieces::numeric<>held OR b.reservation_version<>revision THEN
  RAISE EXCEPTION 'reserved balance correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_balance_integrity';
 END IF;
END;
$$;
CREATE FUNCTION reservation_assert_order(p_owner UUID,p_order UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE h reservation_orders%ROWTYPE; r stock_reservations%ROWTYPE; latest BIGINT;
BEGIN
 SELECT * INTO h FROM reservation_orders WHERE owner_machine_id=p_owner AND order_id=p_order;
 SELECT * INTO r FROM stock_reservations WHERE id=h.current_reservation_id;
 SELECT max(order_version) INTO latest FROM reservation_transitions WHERE owner_machine_id=p_owner AND order_id=p_order;
 IF r.id IS NULL OR (h.owner_machine_id,h.order_id,h.generation,h.version) IS DISTINCT FROM (r.owner_machine_id,r.order_id,r.generation,r.order_version) OR
 h.version IS DISTINCT FROM latest OR h.generation<>(SELECT max(generation) FROM stock_reservations WHERE owner_machine_id=p_owner AND order_id=p_order) THEN
  RAISE EXCEPTION 'reservation order correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_order_integrity';
 END IF;
END;
$$;
CREATE FUNCTION reservation_assert_header(p_id UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE r stock_reservations%ROWTYPE; t reservation_transitions%ROWTYPE; l reservation_lines%ROWTYPE; prior stock_reservations%ROWTYPE;
BEGIN
 SELECT * INTO r FROM stock_reservations WHERE id=p_id;
 SELECT * INTO t FROM reservation_transitions WHERE reservation_id=p_id ORDER BY reservation_version DESC LIMIT 1;
 IF r.id IS NULL OR t.id IS NULL OR (r.state,r.version,r.order_version,r.operation_id) IS DISTINCT FROM (t.state,t.reservation_version,t.order_version,t.operation_id) OR
 (SELECT count(*) FROM reservation_lines WHERE reservation_id=p_id)<>r.line_count OR
 EXISTS(SELECT 1 FROM reservation_lines WHERE reservation_id=p_id AND line_number>r.line_count) OR
 (SELECT count(*) FROM reservation_transitions WHERE reservation_id=p_id)<>r.version OR
 NOT EXISTS(SELECT 1 FROM reservation_transitions WHERE reservation_id=p_id AND reservation_version=1 AND action='RESERVE') THEN
  RAISE EXCEPTION 'reservation history correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_history_integrity';
 END IF;
 IF r.generation>1 THEN
  SELECT * INTO prior FROM stock_reservations WHERE owner_machine_id=r.owner_machine_id AND order_id=r.order_id AND generation=r.generation-1;
  IF prior.id IS NULL OR prior.state<>'RELEASED' OR (SELECT min(order_version) FROM reservation_transitions WHERE reservation_id=r.id)<>prior.order_version+1 THEN
   RAISE EXCEPTION 'reservation generation predecessor mismatch' USING ERRCODE='23514',CONSTRAINT='reservation_generation_integrity';
  END IF;
 ELSE
  IF (SELECT min(order_version) FROM reservation_transitions WHERE reservation_id=r.id)<>1 THEN
   RAISE EXCEPTION 'invalid first generation' USING ERRCODE='23514',CONSTRAINT='reservation_generation_integrity';
  END IF;
 END IF;
 FOR t IN SELECT * FROM reservation_transitions WHERE reservation_id=p_id LOOP
  PERFORM reservation_assert_command(t.owner_machine_id,(SELECT key FROM reservation_commands WHERE owner_machine_id=t.owner_machine_id AND command_id=t.command_id));
 END LOOP;
 FOR l IN SELECT * FROM reservation_lines WHERE reservation_id=p_id LOOP PERFORM reservation_assert_balance(r.warehouse_id,l.variant_id); END LOOP;
 PERFORM reservation_assert_order(r.owner_machine_id,r.order_id);
END;
$$;
CREATE FUNCTION reservation_check_deferred() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='stock_balances' THEN PERFORM reservation_assert_balance(NEW.warehouse_id,NEW.variant_id);
 ELSIF TG_TABLE_NAME='reservation_orders' THEN PERFORM reservation_assert_order(NEW.owner_machine_id,NEW.order_id);
 ELSE
  IF TG_TABLE_NAME='stock_reservations' THEN PERFORM reservation_assert_header(NEW.id);
  ELSE
   IF TG_TABLE_NAME='reservation_commands' THEN PERFORM reservation_assert_command(NEW.owner_machine_id,NEW.key); END IF;
   PERFORM reservation_assert_header(NEW.reservation_id);
  END IF;
 END IF;
 RETURN NULL;
END;
$$;
CREATE FUNCTION reservation_guard_projection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='stock_reservations' THEN
  IF (NEW.id,NEW.owner_machine_id,NEW.order_id,NEW.warehouse_id,NEW.generation,NEW.line_count,NEW.created_at) IS DISTINCT FROM
   (OLD.id,OLD.owner_machine_id,OLD.order_id,OLD.warehouse_id,OLD.generation,OLD.line_count,OLD.created_at) OR OLD.state<>'ACTIVE' OR NEW.state NOT IN('RELEASED','CONSUMED') OR NEW.version<>OLD.version+1 OR NEW.order_version<>OLD.order_version+1 THEN
   RAISE EXCEPTION 'invalid reservation terminal transition' USING ERRCODE='23514',CONSTRAINT='reservation_transition_valid';
  END IF;
 ELSE
  IF (NEW.owner_machine_id,NEW.order_id,NEW.created_at) IS DISTINCT FROM (OLD.owner_machine_id,OLD.order_id,OLD.created_at) OR NEW.version<>OLD.version+1 OR
   NEW.generation NOT IN(OLD.generation,OLD.generation+1) OR (NEW.generation=OLD.generation AND NEW.current_reservation_id IS DISTINCT FROM OLD.current_reservation_id) THEN
   RAISE EXCEPTION 'invalid order head transition' USING ERRCODE='23514',CONSTRAINT='reservation_order_transition';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION reservation_guard_command() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'IN_PROGRESS' THEN RAISE EXCEPTION 'reservation key must be claimed' USING ERRCODE='23514',CONSTRAINT='reservation_key_transition'; END IF;
 ELSE
  IF OLD.state<>'IN_PROGRESS' OR NEW.state<>'COMPLETED' OR
   (NEW.owner_machine_id,NEW.key,NEW.command_id,NEW.reservation_id,NEW.order_id,NEW.warehouse_id,NEW.generation,NEW.action,NEW.fingerprint_version,NEW.request_hash,NEW.created_at) IS DISTINCT FROM
   (OLD.owner_machine_id,OLD.key,OLD.command_id,OLD.reservation_id,OLD.order_id,OLD.warehouse_id,OLD.generation,OLD.action,OLD.fingerprint_version,OLD.request_hash,OLD.created_at) THEN
   RAISE EXCEPTION 'invalid reservation key transition' USING ERRCODE='23514',CONSTRAINT='reservation_key_transition';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER reservations_projection BEFORE UPDATE ON stock_reservations FOR EACH ROW EXECUTE FUNCTION reservation_guard_projection();
CREATE TRIGGER reservation_orders_projection BEFORE UPDATE ON reservation_orders FOR EACH ROW EXECUTE FUNCTION reservation_guard_projection();
CREATE TRIGGER reservation_command_transition BEFORE INSERT OR UPDATE ON reservation_commands FOR EACH ROW EXECUTE FUNCTION reservation_guard_command();
CREATE TRIGGER reservations_no_delete BEFORE DELETE OR TRUNCATE ON stock_reservations FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER reservation_orders_no_delete BEFORE DELETE OR TRUNCATE ON reservation_orders FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER reservation_commands_no_delete BEFORE DELETE OR TRUNCATE ON reservation_commands FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER reservation_lines_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_lines FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER reservation_transitions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_transitions FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE CONSTRAINT TRIGGER reservation_balance_check AFTER INSERT OR UPDATE ON stock_balances DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();
CREATE CONSTRAINT TRIGGER reservation_orders_check AFTER INSERT OR UPDATE ON reservation_orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();
CREATE CONSTRAINT TRIGGER reservation_header_check AFTER INSERT OR UPDATE ON stock_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();
CREATE CONSTRAINT TRIGGER reservation_line_check AFTER INSERT ON reservation_lines DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();
CREATE CONSTRAINT TRIGGER reservation_transition_check AFTER INSERT ON reservation_transitions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();
CREATE CONSTRAINT TRIGGER reservation_command_check AFTER INSERT OR UPDATE ON reservation_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reservation_check_deferred();

CREATE OR REPLACE FUNCTION stock_guard_balance_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.warehouse_id,NEW.variant_id,NEW.base_unit_id,NEW.created_at) IS DISTINCT FROM (OLD.warehouse_id,OLD.variant_id,OLD.base_unit_id,OLD.created_at) OR
 NEW.version<OLD.version OR NEW.reservation_version<OLD.reservation_version OR
 (NEW.version=OLD.version AND NEW.on_hand_pieces<>OLD.on_hand_pieces) OR
 (NEW.reserved_pieces=OLD.reserved_pieces AND NEW.reservation_version<>OLD.reservation_version) OR
 (NEW.reserved_pieces<>OLD.reserved_pieces AND NEW.reservation_version<>OLD.reservation_version+1) OR
 (NEW.version=OLD.version AND NEW.reservation_version=OLD.reservation_version) THEN
  RAISE EXCEPTION 'invalid stock or reserved balance transition' USING ERRCODE='23514',CONSTRAINT='stock_balance_transition';
 END IF;
 RETURN NEW;
END;
$$;

CREATE FUNCTION stock_assert_operation_before_reservations(p_operation UUID) RETURNS void
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

CREATE FUNCTION stock_assert_adjustment_before_reservations(p_id UUID) RETURNS void
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

CREATE OR REPLACE FUNCTION stock_assert_operation(p_operation UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE o stock_operations%ROWTYPE; k stock_idempotency%ROWTYPE;
BEGIN
 PERFORM stock_assert_operation_before_reservations(p_operation);
 SELECT * INTO o FROM stock_operations WHERE id=p_operation;
 SELECT * INTO k FROM stock_idempotency WHERE operation_id=p_operation;
 IF (o.actor_kind,o.actor_machine_id) IS DISTINCT FROM (k.actor_kind,k.actor_machine_id) THEN
  RAISE EXCEPTION 'stock actor correspondence mismatch' USING ERRCODE='23514',CONSTRAINT='stock_actor_integrity';
 END IF;
 IF o.producer='inventory.reservations.consume.v1' THEN PERFORM reservation_assert_consume(p_operation);
 ELSIF o.actor_kind<>'HUMAN' THEN
  RAISE EXCEPTION 'machine cannot use human producer' USING ERRCODE='23514',CONSTRAINT='stock_actor_integrity';
 END IF;
END;
$$;
CREATE OR REPLACE FUNCTION stock_assert_adjustment(p_id UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE o stock_operations%ROWTYPE; k stock_idempotency%ROWTYPE;
BEGIN
 SELECT * INTO o FROM stock_operations WHERE id=p_id;
 SELECT * INTO k FROM stock_idempotency WHERE operation_id=p_id;
 IF o.producer='inventory.reservations.consume.v1' THEN
  IF o.command_version<>3 OR k.fingerprint_version<>3 OR EXISTS(SELECT 1 FROM stock_operation_audit WHERE operation_id=p_id) THEN
   RAISE EXCEPTION 'invalid consume intent' USING ERRCODE='23514',CONSTRAINT='stock_adjustment_integrity';
  END IF;
  PERFORM reservation_assert_consume(p_id);
 ELSE PERFORM stock_assert_adjustment_before_reservations(p_id); END IF;
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
       (NEW.producer,NEW.key,NEW.fingerprint_version,NEW.request_hash,NEW.actor_user_id,NEW.actor_kind,NEW.actor_machine_id,NEW.operation_id,NEW.created_at)
       IS DISTINCT FROM
       (OLD.producer,OLD.key,OLD.fingerprint_version,OLD.request_hash,OLD.actor_user_id,OLD.actor_kind,OLD.actor_machine_id,OLD.operation_id,OLD.created_at) THEN
        RAISE EXCEPTION 'invalid stock idempotency transition'
            USING ERRCODE = '23514', CONSTRAINT = 'stock_key_transition';
    END IF;
    RETURN NEW;
END;
$$;
COMMIT;
