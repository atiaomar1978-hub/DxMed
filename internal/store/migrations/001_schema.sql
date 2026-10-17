-- DxMed schema (runs as med_owner; schema med is created by scripts/infra-setup.sh).

-- configuration --------------------------------------------------------------------------------------------
CREATE TABLE med.config_object (
  kind       text NOT NULL CHECK (kind IN ('stream','decoding_template','field_dictionary','rule_set',
               'transform_group','lookup_table','merge_profile','route','output_template')),
  name       text NOT NULL,
  version    int  NOT NULL,
  doc        jsonb,                       -- NULL marks a deletion
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, name, version));

CREATE VIEW med.config_current AS
  SELECT DISTINCT ON (kind, name) kind, name, version, doc, updated_by, updated_at
  FROM med.config_object ORDER BY kind, name, version DESC;

CREATE TABLE med.release (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  stream        text NOT NULL,
  number        int  NOT NULL,
  bundle        jsonb NOT NULL,
  bundle_sha256 text NOT NULL,
  note          text NOT NULL DEFAULT '',
  published_by  text NOT NULL,
  published_at  timestamptz NOT NULL DEFAULT now(),
  active        boolean NOT NULL DEFAULT false,
  UNIQUE (stream, number));
CREATE UNIQUE INDEX release_active ON med.release (stream) WHERE active;

-- processing -----------------------------------------------------------------------------------------------
CREATE TABLE med.file (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  stream_id    text NOT NULL,
  source       text NOT NULL,
  name         text NOT NULL,
  path         text NOT NULL,
  size         bigint NOT NULL,
  sha256       text NOT NULL,
  exchange_id  text,
  status       text NOT NULL DEFAULT 'NEW' CHECK (status IN
                 ('NEW','LEASED','COMMITTED','POSTED','FAILED','DEAD','DUPLICATE','HELD','CANCELLED')),
  priority     smallint NOT NULL DEFAULT 5,
  attempts     int NOT NULL DEFAULT 0,
  not_before   timestamptz NOT NULL DEFAULT now(),
  lease_owner  text,
  lease_until  timestamptz,
  release_id   uuid REFERENCES med.release(id),
  records_in   bigint, records_out bigint, records_err bigint, records_drop bigint, records_held bigint,
  records_dup  bigint,
  dup_of       uuid,
  error_code   text,
  error        text,
  received_at  timestamptz NOT NULL DEFAULT now(),
  started_at   timestamptz,
  committed_at timestamptz,
  UNIQUE (source, name));
CREATE INDEX file_queue ON med.file (priority, received_at) WHERE status = 'NEW';
CREATE INDEX file_lease ON med.file (lease_until) WHERE status = 'LEASED';
CREATE INDEX file_stream_time ON med.file (stream_id, received_at DESC);
CREATE INDEX file_sha ON med.file (stream_id, sha256);
CREATE INDEX file_name ON med.file (stream_id, name);

CREATE TABLE med.file_event (
  id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  file_id uuid NOT NULL REFERENCES med.file(id) ON DELETE CASCADE,
  ts      timestamptz NOT NULL DEFAULT now(),
  status  text NOT NULL,
  actor   text NOT NULL,
  detail  text NOT NULL DEFAULT '');
CREATE INDEX file_event_file ON med.file_event (file_id, ts);

CREATE TABLE med.file_seq_range (
  file_id     uuid PRIMARY KEY REFERENCES med.file(id) ON DELETE CASCADE,
  stream_id   text NOT NULL,
  exchange_id text NOT NULL DEFAULT '',
  seq_from    bigint NOT NULL DEFAULT 0,
  seq_to      bigint NOT NULL DEFAULT 0,
  time_from   timestamptz,
  time_to     timestamptz);
CREATE INDEX file_seq_range_x ON med.file_seq_range (stream_id, exchange_id, seq_from, seq_to);

CREATE TABLE med.file_stat (
  day        date NOT NULL,
  file_id    uuid,                         -- NULL for scheduler flushes
  stream_id  text NOT NULL,
  stage      text NOT NULL,
  call_type  text NOT NULL,
  action     text NOT NULL,
  reason     text NOT NULL,
  cnt        bigint NOT NULL,
  duration_s numeric NOT NULL DEFAULT 0,
  volume     numeric NOT NULL DEFAULT 0) PARTITION BY RANGE (day);
CREATE INDEX file_stat_day ON med.file_stat (day, stream_id);
CREATE INDEX file_stat_file ON med.file_stat (file_id);

CREATE TABLE med.output_file (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  file_id    uuid REFERENCES med.file(id) ON DELETE SET NULL,
  stream_id  text NOT NULL,
  route      text NOT NULL,
  name       text NOT NULL,
  path       text NOT NULL,
  tmp_path   text NOT NULL,
  records    bigint NOT NULL,
  bytes      bigint NOT NULL,
  sha256     text NOT NULL,
  visible    boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX output_file_file ON med.output_file (file_id);
CREATE INDEX output_file_pending ON med.output_file (created_at) WHERE NOT visible;
CREATE INDEX output_file_route ON med.output_file (stream_id, route, created_at DESC);

CREATE TABLE med.route_seq (
  stream_id text NOT NULL,
  route     text NOT NULL,
  next_seq  bigint NOT NULL,
  PRIMARY KEY (stream_id, route));

CREATE TABLE med.record_key (
  day       date NOT NULL,
  stream_id text NOT NULL,
  key_hash  bytea NOT NULL,
  file_id   uuid NOT NULL,
  PRIMARY KEY (day, stream_id, key_hash)) PARTITION BY RANGE (day);
CREATE INDEX record_key_lookup ON med.record_key (stream_id, key_hash);

CREATE TABLE med.partial_record (
  stream_id  text NOT NULL,
  key_hash   bytea NOT NULL,
  release_id uuid NOT NULL,
  first_seen timestamptz NOT NULL,
  last_seen  timestamptz NOT NULL,
  cnt        int NOT NULL,
  payload    bytea NOT NULL,
  PRIMARY KEY (stream_id, key_hash));
CREATE INDEX partial_record_age ON med.partial_record (stream_id, first_seen);

CREATE TABLE med.error_record (
  day          date NOT NULL,
  file_id      uuid,
  stream_id    text NOT NULL,
  seq          bigint NOT NULL,
  offset_bytes bigint NOT NULL DEFAULT 0,
  stage        text NOT NULL,
  reason       text NOT NULL,
  detail       jsonb,
  raw          bytea) PARTITION BY RANGE (day);
CREATE INDEX error_record_file ON med.error_record (file_id, seq);
CREATE INDEX error_record_reason ON med.error_record (day, stream_id, reason);

CREATE TABLE med.post_action_job (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  output_file_id uuid NOT NULL REFERENCES med.output_file(id) ON DELETE CASCADE,
  step           int NOT NULL,
  op             jsonb NOT NULL,
  status         text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','DONE','FAILED','DEAD')),
  attempts       int NOT NULL DEFAULT 0,
  not_before     timestamptz NOT NULL DEFAULT now(),
  lease_until    timestamptz,
  last_error     text,
  UNIQUE (output_file_id, step));
CREATE INDEX post_action_queue ON med.post_action_job (not_before) WHERE status IN ('PENDING','FAILED');

-- observability & security ---------------------------------------------------------------------------------
CREATE TABLE med.event_log (
  ts        timestamptz NOT NULL DEFAULT now(),
  level     text NOT NULL,
  component text NOT NULL,
  stream_id text,
  file_id   uuid,
  msg       text NOT NULL,
  attrs     jsonb) PARTITION BY RANGE (ts);
CREATE INDEX event_log_ts ON med.event_log (ts DESC);

CREATE TABLE med.audit_log (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  ts          timestamptz NOT NULL DEFAULT now(),
  actor       text NOT NULL,
  action      text NOT NULL,
  object_kind text NOT NULL DEFAULT '',
  object_name text NOT NULL DEFAULT '',
  before      jsonb,
  after       jsonb,
  ip          text NOT NULL DEFAULT '');
CREATE INDEX audit_log_ts ON med.audit_log (ts DESC);

CREATE TABLE med.alarm (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  rule       text NOT NULL,
  severity   text NOT NULL CHECK (severity IN ('info','minor','major','critical')),
  stream_id  text NOT NULL DEFAULT '',
  message    text NOT NULL,
  raised_at  timestamptz NOT NULL DEFAULT now(),
  acked_by   text,
  acked_at   timestamptz,
  cleared_at timestamptz);
CREATE UNIQUE INDEX alarm_open ON med.alarm (rule, stream_id) WHERE cleared_at IS NULL;

CREATE TABLE med.app_user (
  username   text PRIMARY KEY,
  pw_hash    text NOT NULL,
  roles      text[] NOT NULL,
  disabled   boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_login timestamptz);

CREATE TABLE med.session (
  token_hash bytea PRIMARY KEY,
  username   text NOT NULL REFERENCES med.app_user(username) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  ip         text NOT NULL DEFAULT '');
CREATE INDEX session_expiry ON med.session (expires_at);

CREATE TABLE med.worker (
  id        text PRIMARY KEY,
  component text NOT NULL,
  host      text NOT NULL,
  pid       int NOT NULL,
  started   timestamptz NOT NULL,
  seen      timestamptz NOT NULL,
  info      jsonb);

CREATE TABLE med.leader (
  name   text PRIMARY KEY,
  holder text NOT NULL,
  until  timestamptz NOT NULL);

CREATE TABLE med.setting (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now());

CREATE TABLE med.lab_case (
  stream     text NOT NULL,
  name       text NOT NULL,
  sample     bytea NOT NULL,
  file_name  text NOT NULL,
  expected   jsonb NOT NULL,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (stream, name));

-- partition management (callable by med_app; runs with the owner's rights) -------------------------------
CREATE FUNCTION med.ensure_partitions(from_day date, to_day date) RETURNS int
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, med AS $$
DECLARE
  t text; d date; n int := 0; part text;
BEGIN
  FOREACH t IN ARRAY ARRAY['file_stat','record_key','error_record','event_log'] LOOP
    IF NOT EXISTS (SELECT FROM pg_class WHERE relname = t || '_default' AND relnamespace = 'med'::regnamespace) THEN
      EXECUTE format('CREATE TABLE med.%I PARTITION OF med.%I DEFAULT', t || '_default', t);
    END IF;
    d := from_day;
    WHILE d <= to_day LOOP
      part := t || '_p' || to_char(d, 'YYYYMMDD');
      IF NOT EXISTS (SELECT FROM pg_class WHERE relname = part AND relnamespace = 'med'::regnamespace) THEN
        IF t = 'event_log' THEN
          EXECUTE format('CREATE TABLE med.%I PARTITION OF med.%I FOR VALUES FROM (%L) TO (%L)',
            part, t, d::timestamptz, (d + 1)::timestamptz);
        ELSE
          EXECUTE format('CREATE TABLE med.%I PARTITION OF med.%I FOR VALUES FROM (%L) TO (%L)', part, t, d, d + 1);
        END IF;
        n := n + 1;
      END IF;
      d := d + 1;
    END LOOP;
  END LOOP;
  RETURN n;
END $$;

CREATE FUNCTION med.drop_partitions(tbl text, before_day date) RETURNS int
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, med AS $$
DECLARE
  r record; n int := 0;
BEGIN
  IF tbl NOT IN ('file_stat','record_key','error_record','event_log') THEN
    RAISE EXCEPTION 'not a partitioned table: %', tbl;
  END IF;
  FOR r IN SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
           WHERE i.inhparent = ('med.' || tbl)::regclass AND c.relname ~ ('^' || tbl || '_p[0-9]{8}$') LOOP
    IF to_date(right(r.relname, 8), 'YYYYMMDD') < before_day THEN
      EXECUTE format('DROP TABLE med.%I', r.relname);
      n := n + 1;
    END IF;
  END LOOP;
  RETURN n;
END $$;

REVOKE ALL ON FUNCTION med.ensure_partitions(date, date), med.drop_partitions(text, date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION med.ensure_partitions(date, date), med.drop_partitions(text, date) TO med_app;

-- privileges -----------------------------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA med TO med_app;
GRANT SELECT ON ALL TABLES IN SCHEMA med TO med_reader;
REVOKE SELECT ON med.app_user, med.session FROM med_reader;
ALTER DEFAULT PRIVILEGES FOR ROLE med_owner IN SCHEMA med GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO med_app;
ALTER DEFAULT PRIVILEGES FOR ROLE med_owner IN SCHEMA med GRANT SELECT ON TABLES TO med_reader;
