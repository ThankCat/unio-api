-- runtime_control_operations 承载单目标可恢复发布：Channel 四维限额、四个关键 app_settings
-- （route_rate_limit_defaults / channel_rate_limit_defaults / concurrency_defaults /
-- circuit_breaker / routing_balance），以及维护专用
-- 完整性 epoch（gateway.runtime_state_epoch）。Origin/Provider 批量围栏走 origin_routing_operations，
-- 二者不合表（计划 §4.5）。普通状态机：preparing -> prepared -> db_committed -> committed；
-- 非 bootstrap epoch 在 ready 数据态后保持 db_committed -> awaiting_release，真实 Gateway smoke 通过后才 committed；
-- 普通 control 仅业务 revision 未提交时 preparing|prepared -> aborted；runtime_state_epoch 任何阶段不允许 Abort。
CREATE FUNCTION public.runtime_control_epoch_transition_valid(
    transition jsonb,
    current_revision bigint,
    next_revision bigint
) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE STRICT
AS $$
DECLARE
    reason text;
    detected_at timestamptz;
    not_before timestamptz;
BEGIN
    IF jsonb_typeof(transition) <> 'object'
       OR NOT transition ?& ARRAY[
           'recovery_id', 'old_epoch', 'old_revision', 'new_epoch', 'new_revision',
           'reason', 'state_loss_confirmed', 'detected_at', 'not_before'
       ]
       OR transition - ARRAY[
           'recovery_id', 'old_epoch', 'old_revision', 'new_epoch', 'new_revision',
           'reason', 'state_loss_confirmed', 'detected_at', 'not_before'
       ]::text[] <> '{}'::jsonb
       OR jsonb_typeof(transition -> 'new_epoch') <> 'string'
       OR (transition ->> 'new_epoch') !~ '^[0-9a-f]{32}$'
       OR jsonb_typeof(transition -> 'new_revision') <> 'number'
       OR (transition ->> 'new_revision')::bigint <> next_revision
       OR transition -> 'state_loss_confirmed' <> 'true'::jsonb
       OR jsonb_typeof(transition -> 'detected_at') <> 'string'
       OR jsonb_typeof(transition -> 'not_before') <> 'string'
    THEN
        RETURN FALSE;
    END IF;

    reason := transition ->> 'reason';
    detected_at := (transition ->> 'detected_at')::timestamptz;
    not_before := (transition ->> 'not_before')::timestamptz;
    IF not_before < detected_at OR not_before > detected_at + interval '24 hours' THEN
        RETURN FALSE;
    END IF;

    IF reason = 'bootstrap' THEN
        RETURN transition -> 'recovery_id' = 'null'::jsonb
           AND transition -> 'old_epoch' = 'null'::jsonb
           AND transition -> 'old_revision' = 'null'::jsonb
           AND current_revision = 0
           AND next_revision = 1;
    END IF;
    IF reason NOT IN ('state_loss', 'restore') THEN
        RETURN FALSE;
    END IF;
    RETURN jsonb_typeof(transition -> 'recovery_id') = 'string'
       AND (transition ->> 'recovery_id') ~ '^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$'
       AND jsonb_typeof(transition -> 'old_epoch') = 'string'
       AND (transition ->> 'old_epoch') ~ '^[0-9a-f]{32}$'
       AND transition ->> 'old_epoch' <> transition ->> 'new_epoch'
       AND jsonb_typeof(transition -> 'old_revision') = 'number'
       AND (transition ->> 'old_revision')::bigint = current_revision
       AND current_revision >= 1
       AND next_revision = current_revision + 1;
EXCEPTION WHEN OTHERS THEN
    RETURN FALSE;
END;
$$;

CREATE FUNCTION public.runtime_control_recovery_evidence_valid(
    evidence jsonb,
    transition jsonb,
    current_revision bigint,
    operation_state text
) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
AS $$
DECLARE
    gate_name text;
    gate jsonb;
    evidence_status text;
    detected_at timestamptz;
    not_before timestamptz;
    recorded_at timestamptz;
    checked_at timestamptz;
BEGIN
    IF transition ->> 'reason' = 'bootstrap' THEN
        RETURN evidence IS NULL;
    END IF;
    IF evidence IS NULL OR jsonb_typeof(evidence) <> 'object'
       OR NOT evidence ?& ARRAY[
           'schema_version', 'recovery_id', 'current_revision', 'reason', 'detected_at',
           'not_before', 'operator_ref', 'status', 'recorded_at', 'gates'
       ]
       OR evidence - ARRAY[
           'schema_version', 'recovery_id', 'current_revision', 'reason', 'detected_at',
           'not_before', 'operator_ref', 'status', 'recorded_at', 'gates'
       ]::text[] <> '{}'::jsonb
       OR evidence -> 'schema_version' <> to_jsonb(1)
       OR evidence -> 'recovery_id' <> transition -> 'recovery_id'
       OR evidence -> 'current_revision' <> to_jsonb(current_revision)
       OR evidence -> 'reason' <> transition -> 'reason'
       OR evidence -> 'detected_at' <> transition -> 'detected_at'
       OR evidence -> 'not_before' <> transition -> 'not_before'
       OR jsonb_typeof(evidence -> 'operator_ref') <> 'string'
       OR (evidence ->> 'operator_ref') !~ '^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$'
       OR jsonb_typeof(evidence -> 'recorded_at') <> 'string'
       OR jsonb_typeof(evidence -> 'gates') <> 'object'
       OR NOT (evidence -> 'gates') ?& ARRAY[
           'ingress_closed', 'drain', 'window', 'breaker_cooldown', 'permission',
           'control', 'offline_scripts', 'maintenance_probe'
       ]
       OR (evidence -> 'gates') - ARRAY[
           'ingress_closed', 'drain', 'window', 'breaker_cooldown', 'permission',
           'control', 'offline_scripts', 'maintenance_probe'
       ]::text[] <> '{}'::jsonb
    THEN
        RETURN FALSE;
    END IF;

    evidence_status := evidence ->> 'status';
    IF evidence_status NOT IN ('collecting', 'approved')
       OR (operation_state IN ('awaiting_release', 'committed') AND evidence_status <> 'approved')
    THEN
        RETURN FALSE;
    END IF;
    detected_at := (evidence ->> 'detected_at')::timestamptz;
    not_before := (evidence ->> 'not_before')::timestamptz;
    recorded_at := (evidence ->> 'recorded_at')::timestamptz;
    IF not_before < detected_at OR not_before > detected_at + interval '24 hours'
       OR recorded_at < detected_at
    THEN
        RETURN FALSE;
    END IF;

    FOREACH gate_name IN ARRAY ARRAY[
        'ingress_closed', 'drain', 'window', 'breaker_cooldown', 'permission',
        'control', 'offline_scripts', 'maintenance_probe'
    ] LOOP
        gate := evidence -> 'gates' -> gate_name;
        IF jsonb_typeof(gate) <> 'object'
           OR NOT gate ?& ARRAY['status', 'checked_at', 'summary_hash']
           OR gate - ARRAY['status', 'checked_at', 'summary_hash']::text[] <> '{}'::jsonb
        THEN
            RETURN FALSE;
        END IF;
        IF evidence_status = 'collecting' THEN
            IF gate ->> 'status' <> 'pending'
               OR gate -> 'checked_at' <> 'null'::jsonb
               OR gate -> 'summary_hash' <> 'null'::jsonb
            THEN
                RETURN FALSE;
            END IF;
        ELSE
            IF gate ->> 'status' <> 'passed'
               OR jsonb_typeof(gate -> 'checked_at') <> 'string'
               OR jsonb_typeof(gate -> 'summary_hash') <> 'string'
               OR (gate ->> 'summary_hash') !~ '^[0-9a-f]{64}$'
            THEN
                RETURN FALSE;
            END IF;
            checked_at := (gate ->> 'checked_at')::timestamptz;
            IF checked_at < detected_at OR checked_at > recorded_at THEN
                RETURN FALSE;
            END IF;
            IF gate_name = 'window' AND checked_at < not_before THEN
                RETURN FALSE;
            END IF;
        END IF;
    END LOOP;
    RETURN TRUE;
EXCEPTION WHEN OTHERS THEN
    RETURN FALSE;
END;
$$;

CREATE FUNCTION public.runtime_control_release_evidence_valid(
    evidence jsonb,
    transition jsonb,
    operation_state text
) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE
AS $$
BEGIN
    IF transition ->> 'reason' = 'bootstrap' OR operation_state <> 'committed' THEN
        RETURN evidence IS NULL;
    END IF;
    RETURN evidence IS NOT NULL
       AND jsonb_typeof(evidence) = 'object'
       AND evidence ?& ARRAY['schema_version', 'recovery_id', 'revision', 'status', 'checked_at', 'summary_hash']
       AND evidence - ARRAY['schema_version', 'recovery_id', 'revision', 'status', 'checked_at', 'summary_hash']::text[] = '{}'::jsonb
       AND evidence -> 'schema_version' = to_jsonb(1)
       AND evidence -> 'recovery_id' = transition -> 'recovery_id'
       AND evidence -> 'revision' = transition -> 'new_revision'
       AND evidence ->> 'status' = 'passed'
       AND jsonb_typeof(evidence -> 'checked_at') = 'string'
       AND (evidence ->> 'checked_at')::timestamptz IS NOT NULL
       AND jsonb_typeof(evidence -> 'summary_hash') = 'string'
       AND (evidence ->> 'summary_hash') ~ '^[0-9a-f]{64}$';
EXCEPTION WHEN OTHERS THEN
    RETURN FALSE;
END;
$$;

CREATE SEQUENCE public.runtime_control_operations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.runtime_control_operations (
    -- id: 主键。--
    id bigint NOT NULL,
    -- token: 全局唯一安全随机操作令牌。--
    token text NOT NULL,
    -- kind: 操作类型（channel_admission_limits | app_setting | runtime_state_epoch）。--
    kind text NOT NULL,
    -- channel_id: channel_admission_limits 专用目标；其余 kind 为 NULL。--
    channel_id bigint,
    -- setting_key: app_setting/runtime_state_epoch 目标 key；channel_admission_limits 为 NULL。--
    setting_key text,
    -- current_revision: 当前业务 revision（普通发布 >=0，epoch 用作 epoch revision，bootstrap 为 0）。--
    current_revision bigint NOT NULL,
    -- next_revision: 目标 revision，必须等于 current_revision + 1。--
    next_revision bigint NOT NULL,
    -- payload_hash: 规范化目标 payload（epoch 为不可变 transition envelope）的小写 SHA-256。--
    payload_hash text NOT NULL,
    -- epoch_transition: runtime_state_epoch 专用不可变 transition JSONB；其余 kind 必须为 NULL。--
    epoch_transition jsonb,
    -- expected_marker_hash: epoch 恢复所需的 observed marker 期望摘要（absent 或 canonical hash）。--
    expected_marker_hash text,
    -- recovery_evidence: epoch 恢复证据 JSONB（drain/窗口/breaker/permission/control/probe 摘要）。--
    recovery_evidence jsonb,
    -- release_evidence: post-commit Gateway smoke 摘要；仅非 bootstrap epoch released 终态非空。--
    release_evidence jsonb,
    -- state: 操作状态机当前值。--
    state text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- completed_at: 仅在 committed|aborted 终态非空。--
    completed_at timestamp with time zone,
    CONSTRAINT runtime_control_operations_token_check CHECK ((token <> ''::text)),
    CONSTRAINT runtime_control_operations_payload_hash_check CHECK ((payload_hash <> ''::text)),
    CONSTRAINT runtime_control_operations_kind_check CHECK ((kind = ANY (ARRAY['channel_admission_limits'::text, 'app_setting'::text, 'runtime_state_epoch'::text]))),
    CONSTRAINT runtime_control_operations_state_check CHECK ((state = ANY (ARRAY['preparing'::text, 'prepared'::text, 'db_committed'::text, 'awaiting_release'::text, 'committed'::text, 'aborted'::text]))),
    CONSTRAINT runtime_control_operations_revision_check CHECK (((current_revision >= 0) AND (next_revision = current_revision + 1))),
    CONSTRAINT ck_runtime_control_operations_completed_at CHECK (((state = ANY (ARRAY['committed'::text, 'aborted'::text])) = (completed_at IS NOT NULL))),
    CONSTRAINT ck_runtime_control_operations_target CHECK ((
        ((kind = 'channel_admission_limits'::text) AND (channel_id IS NOT NULL) AND (setting_key IS NULL))
        OR ((kind = 'app_setting'::text) AND (channel_id IS NULL) AND (setting_key = ANY (ARRAY['gateway.route_rate_limit_defaults'::text, 'gateway.channel_rate_limit_defaults'::text, 'gateway.concurrency_defaults'::text, 'gateway.circuit_breaker'::text, 'gateway.routing_balance'::text])))
        OR ((kind = 'runtime_state_epoch'::text) AND (channel_id IS NULL) AND (setting_key = 'gateway.runtime_state_epoch'::text))
    )),
    CONSTRAINT ck_runtime_control_operations_epoch_cols CHECK ((
        ((kind = 'runtime_state_epoch'::text) AND (epoch_transition IS NOT NULL) AND state <> 'aborted')
        OR ((kind <> 'runtime_state_epoch'::text) AND (epoch_transition IS NULL) AND (expected_marker_hash IS NULL) AND (recovery_evidence IS NULL) AND (release_evidence IS NULL) AND state <> 'awaiting_release')
    )),
    CONSTRAINT ck_runtime_control_operations_epoch_transition CHECK (
        kind <> 'runtime_state_epoch'
        OR public.runtime_control_epoch_transition_valid(epoch_transition, current_revision, next_revision)
    ),
    CONSTRAINT ck_runtime_control_operations_recovery_evidence_shape CHECK (
        kind <> 'runtime_state_epoch'
        OR public.runtime_control_recovery_evidence_valid(recovery_evidence, epoch_transition, current_revision, state)
    ),
    CONSTRAINT ck_runtime_control_operations_release_evidence_shape CHECK (
        kind <> 'runtime_state_epoch'
        OR public.runtime_control_release_evidence_valid(release_evidence, epoch_transition, state)
    )
);

CREATE FUNCTION public.enforce_runtime_control_operation_transition() RETURNS trigger
    LANGUAGE plpgsql
AS $$
DECLARE
    epoch_value jsonb;
    epoch_revision bigint;
    epoch_activated_at timestamptz;
    evidence_time timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'preparing' THEN
            RAISE EXCEPTION 'runtime control operation must start in preparing';
        END IF;
        IF NEW.kind = 'runtime_state_epoch'
           AND NEW.epoch_transition ->> 'reason' IN ('state_loss', 'restore')
        THEN
            SELECT value, revision
            INTO epoch_value, epoch_revision
            FROM public.app_settings
            WHERE key = 'gateway.runtime_state_epoch';
            epoch_activated_at := (epoch_value ->> 'activated_at')::timestamptz;
            evidence_time := (NEW.epoch_transition ->> 'detected_at')::timestamptz;
            IF epoch_revision <> NEW.current_revision
               OR epoch_value ->> 'epoch' <> NEW.epoch_transition ->> 'old_epoch'
               OR epoch_value ->> 'state' <> 'ready'
               OR epoch_activated_at IS NULL
               OR evidence_time < epoch_activated_at
            THEN
                RAISE EXCEPTION 'runtime state epoch recovery does not match current ready epoch';
            END IF;
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.token IS DISTINCT FROM OLD.token
       OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.channel_id IS DISTINCT FROM OLD.channel_id
       OR NEW.setting_key IS DISTINCT FROM OLD.setting_key
       OR NEW.current_revision IS DISTINCT FROM OLD.current_revision
       OR NEW.next_revision IS DISTINCT FROM OLD.next_revision
       OR NEW.payload_hash IS DISTINCT FROM OLD.payload_hash
       OR NEW.epoch_transition IS DISTINCT FROM OLD.epoch_transition
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'runtime control operation immutable fields changed';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
        (OLD.state = 'preparing' AND NEW.state IN ('prepared', 'aborted'))
        OR (OLD.state = 'prepared' AND NEW.state IN ('db_committed', 'aborted'))
        OR (OLD.state = 'db_committed' AND NEW.state = 'committed' AND NEW.kind <> 'runtime_state_epoch')
        OR (OLD.state = 'db_committed' AND NEW.state = 'committed' AND NEW.kind = 'runtime_state_epoch' AND NEW.epoch_transition ->> 'reason' = 'bootstrap')
        OR (OLD.state = 'db_committed' AND NEW.state = 'awaiting_release' AND NEW.kind = 'runtime_state_epoch' AND NEW.epoch_transition ->> 'reason' IN ('state_loss', 'restore'))
        OR (OLD.state = 'awaiting_release' AND NEW.state = 'committed' AND NEW.kind = 'runtime_state_epoch')
    ) THEN
        RAISE EXCEPTION 'invalid runtime control operation state transition: % -> %', OLD.state, NEW.state;
    END IF;

    IF NEW.kind = 'runtime_state_epoch' AND NEW.state = 'aborted' THEN
        RAISE EXCEPTION 'runtime state epoch operation cannot be aborted';
    END IF;
    IF NEW.recovery_evidence IS DISTINCT FROM OLD.recovery_evidence AND NOT (
        NEW.kind = 'runtime_state_epoch'
        AND OLD.state = 'db_committed'
        AND NEW.state = 'db_committed'
        AND OLD.recovery_evidence ->> 'status' IN ('collecting', 'approved')
        AND NEW.recovery_evidence ->> 'status' = 'approved'
    ) THEN
        RAISE EXCEPTION 'invalid runtime state epoch recovery evidence transition';
    END IF;
    IF NEW.release_evidence IS DISTINCT FROM OLD.release_evidence AND NOT (
        NEW.kind = 'runtime_state_epoch'
        AND OLD.state = 'awaiting_release'
        AND NEW.state = 'committed'
        AND OLD.release_evidence IS NULL
        AND NEW.release_evidence IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'invalid runtime state epoch release evidence transition';
    END IF;

    IF OLD.state = 'awaiting_release' AND NEW.state = 'committed' THEN
        SELECT value, revision
        INTO epoch_value, epoch_revision
        FROM public.app_settings
        WHERE key = 'gateway.runtime_state_epoch';
        epoch_activated_at := (epoch_value ->> 'activated_at')::timestamptz;
        evidence_time := (NEW.release_evidence ->> 'checked_at')::timestamptz;
        IF epoch_revision <> NEW.next_revision THEN
            RAISE EXCEPTION 'state epoch release revision does not match ready epoch';
        ELSIF epoch_value ->> 'epoch' <> NEW.epoch_transition ->> 'new_epoch'
           OR epoch_value ->> 'state' <> 'ready' THEN
            RAISE EXCEPTION 'state epoch release identity does not match ready epoch';
        ELSIF evidence_time < epoch_activated_at THEN
            RAISE EXCEPTION 'state epoch release smoke predates ready epoch';
        ELSIF evidence_time > clock_timestamp() THEN
            RAISE EXCEPTION 'state epoch release smoke timestamp is in the future';
        ELSIF evidence_time < clock_timestamp() - interval '15 minutes' THEN
            RAISE EXCEPTION 'state epoch release smoke evidence is stale';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_runtime_control_operation_transition
    BEFORE INSERT OR UPDATE ON public.runtime_control_operations
    FOR EACH ROW EXECUTE FUNCTION public.enforce_runtime_control_operation_transition();

ALTER SEQUENCE public.runtime_control_operations_id_seq OWNED BY public.runtime_control_operations.id;

ALTER TABLE ONLY public.runtime_control_operations ALTER COLUMN id SET DEFAULT nextval('public.runtime_control_operations_id_seq'::regclass);

ALTER TABLE ONLY public.runtime_control_operations
    ADD CONSTRAINT runtime_control_operations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.runtime_control_operations
    ADD CONSTRAINT runtime_control_operations_token_key UNIQUE (token);

-- 同一 Channel 同时最多一条非终态 operation。
CREATE UNIQUE INDEX uq_runtime_control_operations_active_channel
    ON public.runtime_control_operations USING btree (channel_id)
    WHERE ((channel_id IS NOT NULL) AND (state <> ALL (ARRAY['committed'::text, 'aborted'::text])));

-- 同一 setting 同时最多一条非终态 operation。
CREATE UNIQUE INDEX uq_runtime_control_operations_active_setting
    ON public.runtime_control_operations USING btree (setting_key)
    WHERE ((setting_key IS NOT NULL) AND (state <> ALL (ARRAY['committed'::text, 'aborted'::text])));

CREATE INDEX idx_runtime_control_operations_state ON public.runtime_control_operations USING btree (state) WHERE (state <> ALL (ARRAY['committed'::text, 'aborted'::text]));

ALTER TABLE ONLY public.runtime_control_operations
    ADD CONSTRAINT runtime_control_operations_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.runtime_control_operations
    ADD CONSTRAINT runtime_control_operations_setting_key_fkey FOREIGN KEY (setting_key) REFERENCES public.app_settings(key) ON DELETE RESTRICT;
