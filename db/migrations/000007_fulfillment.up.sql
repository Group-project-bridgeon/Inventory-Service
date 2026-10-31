BEGIN;
CREATE TABLE dispatches (
 id UUID PRIMARY KEY CHECK(id<>'00000000-0000-0000-0000-000000000000'),
 owner_machine_id UUID NOT NULL,
 reservation_id UUID NOT NULL REFERENCES stock_reservations(id) ON DELETE RESTRICT,
 order_id UUID NOT NULL,
 warehouse_id UUID NOT NULL REFERENCES warehouses(id) ON DELETE RESTRICT,
 generation BIGINT NOT NULL CHECK(generation>0),
 state TEXT NOT NULL CHECK(state IN('DRAFT','DISPATCHED','DELIVERED','CANCELLED')),
 version BIGINT NOT NULL CHECK(version>0),
 operation_id UUID UNIQUE REFERENCES stock_operations(id) ON DELETE RESTRICT,
 external_reference TEXT NOT NULL DEFAULT '' CHECK(char_length(external_reference)<=100),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK((state IN('DRAFT','CANCELLED') AND operation_id IS NULL) OR(state IN('DISPATCHED','DELIVERED') AND operation_id IS NOT NULL))
);
CREATE UNIQUE INDEX fulfillment_live_reservation ON dispatches(reservation_id) WHERE state<>'CANCELLED';
CREATE INDEX fulfillment_owner_page ON dispatches(owner_machine_id,id DESC);
CREATE INDEX fulfillment_warehouse_page ON dispatches(warehouse_id,id DESC);
CREATE INDEX fulfillment_order_page ON dispatches(order_id,id DESC);
CREATE INDEX fulfillment_reservation_page ON dispatches(reservation_id,id DESC);
CREATE TABLE fulfillment_commands (
 actor_kind TEXT NOT NULL CHECK(actor_kind IN('MACHINE','HUMAN')),
 actor_id UUID NOT NULL CHECK(actor_id<>'00000000-0000-0000-0000-000000000000'),
 key UUID NOT NULL CHECK((get_byte(uuid_send(key),6)>>4) IN(4,7) AND (get_byte(uuid_send(key),8)&192)=128),
 command_id UUID NOT NULL CHECK((get_byte(uuid_send(command_id),6)>>4) IN(4,7) AND (get_byte(uuid_send(command_id),8)&192)=128),
 dispatch_id UUID NOT NULL REFERENCES dispatches(id) DEFERRABLE INITIALLY DEFERRED,
 owner_machine_id UUID NOT NULL,
 action TEXT NOT NULL CHECK(action IN('CREATE','CANCEL','DISPATCH','DELIVERY')),
 fingerprint_version SMALLINT NOT NULL CHECK(fingerprint_version=1),
 request_hash BYTEA NOT NULL CHECK(octet_length(request_hash)=32),
 state TEXT NOT NULL DEFAULT 'IN_PROGRESS',result_version SMALLINT,result JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),completed_at TIMESTAMPTZ,
 PRIMARY KEY(actor_kind,actor_id,key),UNIQUE(actor_kind,actor_id,command_id),
 CHECK((actor_kind='MACHINE' AND actor_id=owner_machine_id AND action IN('CREATE','CANCEL')) OR(actor_kind='HUMAN' AND action IN('DISPATCH','DELIVERY'))),
 CHECK((state='IN_PROGRESS' AND result_version IS NULL AND result IS NULL AND completed_at IS NULL) OR(state='COMPLETED' AND result_version=1 AND result_version IS NOT NULL AND result IS NOT NULL AND jsonb_typeof(result)='object' AND completed_at IS NOT NULL))
);
CREATE TABLE fulfillment_transitions (
 id UUID PRIMARY KEY CHECK((get_byte(uuid_send(id),6)>>4)=7 AND (get_byte(uuid_send(id),8)&192)=128),
 dispatch_id UUID NOT NULL REFERENCES dispatches(id) ON DELETE RESTRICT,
 command_id UUID NOT NULL,
 actor_kind TEXT NOT NULL,actor_id UUID NOT NULL,
 action TEXT NOT NULL,state TEXT NOT NULL,
 version BIGINT NOT NULL CHECK(version>0),
 operation_id UUID REFERENCES stock_operations(id) ON DELETE RESTRICT,
 outcome TEXT CHECK(outcome IN('FAILED','DELIVERED')),
 external_reference TEXT NOT NULL DEFAULT '' CHECK(char_length(external_reference)<=100),
 note TEXT NOT NULL DEFAULT '' CHECK(char_length(note)<=500),
 first_request_id TEXT CHECK(first_request_id IS NULL OR char_length(first_request_id)<=64),
 changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(actor_kind,actor_id,command_id) REFERENCES fulfillment_commands(actor_kind,actor_id,command_id) DEFERRABLE INITIALLY DEFERRED,
 UNIQUE(dispatch_id,version),UNIQUE(actor_kind,actor_id,command_id),
 CHECK((action='CREATE' AND state='DRAFT' AND version=1 AND operation_id IS NULL AND outcome IS NULL) OR
 (action='CANCEL' AND state='CANCELLED' AND operation_id IS NULL AND outcome IS NULL) OR
 (action='DISPATCH' AND state='DISPATCHED' AND operation_id IS NOT NULL AND outcome IS NULL) OR
 (action='DELIVERY' AND operation_id IS NOT NULL AND ((state='DISPATCHED' AND outcome='FAILED') OR(state='DELIVERED' AND outcome='DELIVERED'))))
);
CREATE INDEX fulfillment_audit_page ON fulfillment_transitions(dispatch_id,id DESC);
ALTER TABLE reservation_commands ADD COLUMN actor_user_id UUID,ADD COLUMN dispatch_id UUID REFERENCES dispatches(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE reservation_commands DROP CONSTRAINT reservation_commands_fingerprint_version_check;
ALTER TABLE reservation_commands ADD CONSTRAINT reservation_command_actor_version CHECK(
 (fingerprint_version=1 AND actor_user_id IS NULL AND dispatch_id IS NULL) OR
 (fingerprint_version=2 AND action='CONSUME' AND actor_user_id IS NOT NULL AND actor_user_id<>'00000000-0000-0000-0000-000000000000' AND dispatch_id IS NOT NULL));
ALTER TABLE reservation_transitions ADD COLUMN actor_user_id UUID,ADD COLUMN dispatch_id UUID REFERENCES dispatches(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE reservation_transitions ADD CONSTRAINT reservation_transition_actor CHECK(
 (actor_user_id IS NULL AND dispatch_id IS NULL) OR(actor_user_id IS NOT NULL AND actor_user_id<>'00000000-0000-0000-0000-000000000000' AND dispatch_id IS NOT NULL AND action='CONSUME'));
ALTER TABLE stock_operations DROP CONSTRAINT stock_operations_command_version_valid;
ALTER TABLE stock_operations ADD CONSTRAINT stock_operations_command_version_valid CHECK(command_version IN(1,2,3,4));
ALTER TABLE stock_idempotency DROP CONSTRAINT stock_idempotency_fingerprint_valid;
ALTER TABLE stock_idempotency ADD CONSTRAINT stock_idempotency_fingerprint_valid CHECK(fingerprint_version IN(1,2,3,4) AND octet_length(request_hash)=32);
-- Copy old function bodies, retaining original OIDs for trigger/cached plan callers.
DO $$ DECLARE n TEXT; definition TEXT; BEGIN
 FOREACH n IN ARRAY ARRAY['stock_assert_operation','stock_assert_adjustment','reservation_assert_consume','reservation_assert_command','reservation_guard_command'] LOOP
  SELECT pg_get_functiondef(p.oid) INTO definition FROM pg_proc p JOIN pg_namespace s ON s.oid=p.pronamespace WHERE s.nspname='public' AND p.proname=n;
  EXECUTE replace(definition,'FUNCTION public.'||n||'(','FUNCTION public.'||n||'_before_fulfillment(');
 END LOOP;
END $$;
CREATE FUNCTION fulfillment_expected_result(p_kind TEXT,p_actor UUID,p_key UUID) RETURNS JSONB LANGUAGE sql STABLE SET search_path=pg_catalog,public AS $$
 SELECT jsonb_build_object('result_version','1','dispatch_id',d.id::text,'reservation_id',d.reservation_id::text,'order_id',d.order_id::text,'warehouse_id',d.warehouse_id::text,'generation',d.generation::text,
 'state',t.state,'version',t.version::text,'changed_at',to_char(t.changed_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),'operation_id',t.operation_id::text,'delivery_outcome',t.outcome,
 'stock_result',CASE WHEN t.action='DISPATCH' THEN stock_expected_result(t.operation_id) ELSE NULL END)
 FROM fulfillment_commands c JOIN fulfillment_transitions t ON(t.actor_kind,t.actor_id,t.command_id)=(c.actor_kind,c.actor_id,c.command_id)
 JOIN dispatches d ON d.id=t.dispatch_id WHERE (c.actor_kind,c.actor_id,c.key)=(p_kind,p_actor,p_key);
$$;
CREATE FUNCTION fulfillment_assert_command(p_kind TEXT,p_actor UUID,p_key UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE c fulfillment_commands%ROWTYPE; t fulfillment_transitions%ROWTYPE; d dispatches%ROWTYPE;
BEGIN
 SELECT * INTO c FROM fulfillment_commands WHERE(actor_kind,actor_id,key)=(p_kind,p_actor,p_key);
 SELECT * INTO t FROM fulfillment_transitions WHERE(actor_kind,actor_id,command_id)=(c.actor_kind,c.actor_id,c.command_id);
 SELECT * INTO d FROM dispatches WHERE id=c.dispatch_id;
 IF c.command_id IS NULL OR t.id IS NULL OR d.id IS NULL OR c.state<>'COMPLETED' OR (c.dispatch_id,c.owner_machine_id,c.action) IS DISTINCT FROM(t.dispatch_id,d.owner_machine_id,t.action) OR c.result IS DISTINCT FROM fulfillment_expected_result(p_kind,p_actor,p_key) THEN
 RAISE EXCEPTION 'fulfillment command mismatch' USING ERRCODE='23514',CONSTRAINT='fulfillment_command_integrity';END IF;
END;$$;
CREATE FUNCTION fulfillment_assert_dispatch(p_id UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE d dispatches%ROWTYPE;r stock_reservations%ROWTYPE;t fulfillment_transitions%ROWTYPE;prev fulfillment_transitions%ROWTYPE;c fulfillment_commands%ROWTYPE;
BEGIN
 SELECT * INTO d FROM dispatches WHERE id=p_id;SELECT * INTO r FROM stock_reservations WHERE id=d.reservation_id;
 SELECT * INTO t FROM fulfillment_transitions WHERE dispatch_id=p_id ORDER BY version DESC LIMIT 1;
 IF d.id IS NULL OR r.id IS NULL OR t.id IS NULL OR(d.owner_machine_id,d.order_id,d.warehouse_id,d.generation) IS DISTINCT FROM(r.owner_machine_id,r.order_id,r.warehouse_id,r.generation) OR
 (d.state,d.version,d.operation_id,d.updated_at) IS DISTINCT FROM(t.state,t.version,t.operation_id,t.changed_at) OR
 (SELECT count(*) FROM fulfillment_transitions WHERE dispatch_id=p_id)<>d.version THEN
 RAISE EXCEPTION 'fulfillment history mismatch' USING ERRCODE='23514',CONSTRAINT='fulfillment_history_integrity';END IF;
 FOR t IN SELECT * FROM fulfillment_transitions WHERE dispatch_id=p_id ORDER BY version LOOP
  IF t.version=1 THEN
   IF t.action<>'CREATE' OR t.actor_kind<>'MACHINE' OR t.actor_id<>d.owner_machine_id OR t.changed_at<>d.created_at OR t.external_reference<>d.external_reference THEN
    RAISE EXCEPTION 'invalid draft origin' USING ERRCODE='23514',CONSTRAINT='fulfillment_history_integrity';END IF;
  ELSE
   IF (prev.state='DRAFT' AND t.action NOT IN('CANCEL','DISPATCH')) OR(prev.state='DISPATCHED' AND t.action<>'DELIVERY') OR prev.state IN('CANCELLED','DELIVERED') OR t.version<>prev.version+1 OR
    (t.action='DELIVERY' AND t.operation_id IS DISTINCT FROM prev.operation_id) THEN
    RAISE EXCEPTION 'invalid lifecycle chain' USING ERRCODE='23514',CONSTRAINT='fulfillment_history_integrity';END IF;
  END IF;
  SELECT * INTO c FROM fulfillment_commands WHERE(actor_kind,actor_id,command_id)=(t.actor_kind,t.actor_id,t.command_id);
  PERFORM fulfillment_assert_command(c.actor_kind,c.actor_id,c.key);
  IF t.action='DISPATCH' THEN
   IF r.state<>'CONSUMED' OR r.operation_id<>t.operation_id OR t.operation_id<>t.command_id THEN
    RAISE EXCEPTION 'dispatch missing consume' USING ERRCODE='23514',CONSTRAINT='fulfillment_consume_integrity';END IF;
   PERFORM reservation_assert_consume(t.operation_id);
  END IF;
  prev:=t;
 END LOOP;
END;$$;
CREATE FUNCTION fulfillment_guard_projection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.owner_machine_id,NEW.reservation_id,NEW.order_id,NEW.warehouse_id,NEW.generation,NEW.external_reference,NEW.created_at) IS DISTINCT FROM
 (OLD.id,OLD.owner_machine_id,OLD.reservation_id,OLD.order_id,OLD.warehouse_id,OLD.generation,OLD.external_reference,OLD.created_at) OR NEW.version<>OLD.version+1 OR
 NOT ((OLD.state='DRAFT' AND NEW.state IN('CANCELLED','DISPATCHED')) OR(OLD.state='DISPATCHED' AND NEW.state IN('DISPATCHED','DELIVERED'))) OR
 (OLD.operation_id IS NOT NULL AND NEW.operation_id IS DISTINCT FROM OLD.operation_id) THEN
 RAISE EXCEPTION 'invalid dispatch projection' USING ERRCODE='23514',CONSTRAINT='fulfillment_projection_valid';END IF;RETURN NEW;
END;$$;
CREATE FUNCTION fulfillment_guard_key() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'IN_PROGRESS' THEN RAISE EXCEPTION 'claim required' USING ERRCODE='23514',CONSTRAINT='fulfillment_key_valid';END IF;
 ELSIF OLD.state<>'IN_PROGRESS' OR NEW.state<>'COMPLETED' OR
 (NEW.actor_kind,NEW.actor_id,NEW.key,NEW.command_id,NEW.dispatch_id,NEW.owner_machine_id,NEW.action,NEW.fingerprint_version,NEW.request_hash,NEW.created_at) IS DISTINCT FROM
 (OLD.actor_kind,OLD.actor_id,OLD.key,OLD.command_id,OLD.dispatch_id,OLD.owner_machine_id,OLD.action,OLD.fingerprint_version,OLD.request_hash,OLD.created_at) THEN
 RAISE EXCEPTION 'invalid fulfillment key transition' USING ERRCODE='23514',CONSTRAINT='fulfillment_key_valid';END IF;RETURN NEW;
END;$$;
CREATE FUNCTION fulfillment_check_deferred() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='dispatches' THEN PERFORM fulfillment_assert_dispatch(NEW.id);
 ELSE
  IF TG_TABLE_NAME='fulfillment_commands' THEN PERFORM fulfillment_assert_command(NEW.actor_kind,NEW.actor_id,NEW.key);END IF;
  PERFORM fulfillment_assert_dispatch(NEW.dispatch_id);
 END IF;RETURN NULL;
END;$$;
CREATE TRIGGER fulfillment_projection BEFORE UPDATE ON dispatches FOR EACH ROW EXECUTE FUNCTION fulfillment_guard_projection();
CREATE TRIGGER fulfillment_dispatch_no_delete BEFORE DELETE OR TRUNCATE ON dispatches FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER fulfillment_key_transition BEFORE INSERT OR UPDATE ON fulfillment_commands FOR EACH ROW EXECUTE FUNCTION fulfillment_guard_key();
CREATE TRIGGER fulfillment_key_no_delete BEFORE DELETE OR TRUNCATE ON fulfillment_commands FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE TRIGGER fulfillment_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON fulfillment_transitions FOR EACH STATEMENT EXECUTE FUNCTION stock_reject_mutation();
CREATE CONSTRAINT TRIGGER fulfillment_header_check AFTER INSERT OR UPDATE ON dispatches DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fulfillment_check_deferred();
CREATE CONSTRAINT TRIGGER fulfillment_command_check AFTER INSERT OR UPDATE ON fulfillment_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fulfillment_check_deferred();
CREATE CONSTRAINT TRIGGER fulfillment_transition_check AFTER INSERT ON fulfillment_transitions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fulfillment_check_deferred();
CREATE OR REPLACE FUNCTION reservation_assert_consume(p_operation UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE o stock_operations%ROWTYPE;r stock_reservations%ROWTYPE;t reservation_transitions%ROWTYPE;d dispatches%ROWTYPE;f fulfillment_transitions%ROWTYPE;
BEGIN
 SELECT * INTO o FROM stock_operations WHERE id=p_operation;
 IF o.producer<>'inventory.dispatches.v1' THEN
  PERFORM reservation_assert_consume_before_fulfillment(p_operation);
  IF EXISTS(SELECT 1 FROM dispatches WHERE reservation_id=o.source_event_id AND state<>'CANCELLED') THEN
   RAISE EXCEPTION 'dispatch association requires confirmation' USING ERRCODE='23514',CONSTRAINT='fulfillment_consume_integrity';END IF;RETURN;
 END IF;
 SELECT * INTO t FROM reservation_transitions WHERE operation_id=p_operation;
 SELECT * INTO r FROM stock_reservations WHERE id=t.reservation_id;
 SELECT * INTO d FROM dispatches WHERE id=t.dispatch_id;
 SELECT * INTO f FROM fulfillment_transitions WHERE dispatch_id=d.id AND action='DISPATCH';
 IF r.id IS NULL OR t.id IS NULL OR d.id IS NULL OR f.id IS NULL OR t.action<>'CONSUME' OR r.state<>'CONSUMED' OR
 (o.producer,o.direction,o.command_version,o.actor_kind,o.actor_user_id,o.source_event_id,o.id,o.warehouse_id,o.line_count) IS DISTINCT FROM
 ('inventory.dispatches.v1','OUT',4::smallint,'HUMAN',t.actor_user_id,r.id,t.command_id,r.warehouse_id,r.line_count) OR
 (d.operation_id,d.reservation_id,d.owner_machine_id,f.command_id,f.actor_kind,f.actor_id,f.operation_id,f.changed_at) IS DISTINCT FROM
 (o.id,r.id,r.owner_machine_id,o.id,'HUMAN',o.actor_user_id,o.id,o.posted_at) OR
 EXISTS(SELECT 1 FROM stock_movements m LEFT JOIN reservation_lines l ON l.reservation_id=r.id AND l.line_number=m.line_number WHERE m.operation_id=o.id AND
 (l.reservation_id IS NULL OR(m.variant_id,m.product_id,m.base_unit_id,m.input_count,m.pieces_per_input,m.pieces,m.pack_id) IS DISTINCT FROM(l.variant_id,l.product_id,l.base_unit_id,l.pieces,1::bigint,l.pieces,NULL::uuid))) THEN
 RAISE EXCEPTION 'invalid fulfillment consume' USING ERRCODE='23514',CONSTRAINT='fulfillment_consume_integrity';END IF;
END;$$;
CREATE OR REPLACE FUNCTION stock_assert_operation(p_operation UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN PERFORM stock_assert_operation_before_fulfillment(p_operation);
 IF EXISTS(SELECT 1 FROM stock_operations WHERE id=p_operation AND producer='inventory.dispatches.v1') THEN PERFORM reservation_assert_consume(p_operation);END IF;
END;$$;
CREATE OR REPLACE FUNCTION stock_assert_adjustment(p_id UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM stock_operations WHERE id=p_id AND producer='inventory.dispatches.v1') THEN
  IF NOT EXISTS(SELECT 1 FROM stock_operations o JOIN stock_idempotency k ON k.operation_id=o.id WHERE o.id=p_id AND o.command_version=4 AND k.fingerprint_version=4) OR EXISTS(SELECT 1 FROM stock_operation_audit WHERE operation_id=p_id) THEN
   RAISE EXCEPTION 'invalid dispatch intent' USING ERRCODE='23514',CONSTRAINT='fulfillment_consume_integrity';END IF;
  PERFORM reservation_assert_consume(p_id);
 ELSE PERFORM stock_assert_adjustment_before_fulfillment(p_id);END IF;
END;$$;
CREATE OR REPLACE FUNCTION reservation_assert_command(p_owner UUID,p_key UUID) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE c reservation_commands%ROWTYPE;t reservation_transitions%ROWTYPE;f fulfillment_commands%ROWTYPE;
BEGIN
 PERFORM reservation_assert_command_before_fulfillment(p_owner,p_key);
 SELECT * INTO c FROM reservation_commands WHERE(owner_machine_id,key)=(p_owner,p_key);
 SELECT * INTO t FROM reservation_transitions WHERE(owner_machine_id,command_id)=(p_owner,c.command_id);
 IF (c.actor_user_id,c.dispatch_id) IS DISTINCT FROM(t.actor_user_id,t.dispatch_id) THEN RAISE EXCEPTION 'reservation actor mismatch' USING ERRCODE='23514',CONSTRAINT='fulfillment_actor_integrity';END IF;
 IF c.fingerprint_version=2 THEN
  SELECT * INTO f FROM fulfillment_commands WHERE actor_kind='HUMAN' AND actor_id=c.actor_user_id AND command_id=c.command_id;
  IF f.command_id IS NULL OR(f.dispatch_id,f.owner_machine_id,f.action,f.key) IS DISTINCT FROM(c.dispatch_id,c.owner_machine_id,'DISPATCH',c.key) THEN
   RAISE EXCEPTION 'missing fulfillment command' USING ERRCODE='23514',CONSTRAINT='fulfillment_actor_integrity';END IF;
 END IF;
END;$$;
CREATE OR REPLACE FUNCTION reservation_guard_command() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN IF NEW.state<>'IN_PROGRESS' THEN RAISE EXCEPTION 'claim required' USING ERRCODE='23514',CONSTRAINT='reservation_key_transition';END IF;
 ELSE IF OLD.state<>'IN_PROGRESS' OR NEW.state<>'COMPLETED' OR
 (NEW.owner_machine_id,NEW.key,NEW.command_id,NEW.reservation_id,NEW.order_id,NEW.warehouse_id,NEW.generation,NEW.action,NEW.fingerprint_version,NEW.request_hash,NEW.created_at,NEW.actor_user_id,NEW.dispatch_id) IS DISTINCT FROM
 (OLD.owner_machine_id,OLD.key,OLD.command_id,OLD.reservation_id,OLD.order_id,OLD.warehouse_id,OLD.generation,OLD.action,OLD.fingerprint_version,OLD.request_hash,OLD.created_at,OLD.actor_user_id,OLD.dispatch_id) THEN
 RAISE EXCEPTION 'invalid reservation key transition' USING ERRCODE='23514',CONSTRAINT='reservation_key_transition';END IF;END IF;RETURN NEW;
END;$$;
COMMIT;
