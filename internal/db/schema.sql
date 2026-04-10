-- schema.sql — memecoin_scorer production schema
-- Apply with: psql $DATABASE_URL -f internal/db/schema.sql
-- Idempotent: all statements use IF NOT EXISTS / OR REPLACE.

-- ============================================================
-- Core token registry
-- ============================================================

CREATE TABLE IF NOT EXISTS tokens (
    mint                 TEXT        PRIMARY KEY,
    first_seen_at        TIMESTAMPTZ NOT NULL,
    last_event_at        TIMESTAMPTZ NOT NULL,
    -- Cumulative volume
    total_buy_sol        NUMERIC     NOT NULL DEFAULT 0,
    total_sell_sol       NUMERIC     NOT NULL DEFAULT 0,
    sell_trade_count     INT         NOT NULL DEFAULT 0,
    total_event_count    INT         NOT NULL DEFAULT 0,
    unique_buyer_count   INT         NOT NULL DEFAULT 0,
    -- 7-gate fields
    liquidity_pool_sol   NUMERIC     NOT NULL DEFAULT 0,
    market_cap_sol       NUMERIC     NOT NULL DEFAULT 0,
    last_price_sol       NUMERIC     NOT NULL DEFAULT 0,
    top10_holder_pct     NUMERIC     NOT NULL DEFAULT 0,
    volume_24h_sol       NUMERIC     NOT NULL DEFAULT 0,
    organic_winner_count INT         NOT NULL DEFAULT 0,
    holders_at_30m       INT         NOT NULL DEFAULT 0,
    holders_at_60m       INT         NOT NULL DEFAULT 0,
    holder_count         INT         NOT NULL DEFAULT 0,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS tokens_last_event_at_idx ON tokens (last_event_at DESC);

-- ============================================================
-- Live token snapshots (one row per evaluation cycle per mint)
-- ============================================================

CREATE TABLE IF NOT EXISTS token_snapshots (
    id                      BIGSERIAL   PRIMARY KEY,
    mint                    TEXT        NOT NULL REFERENCES tokens(mint),
    snapshot_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Live metrics
    buyers_last_1m          INT         NOT NULL DEFAULT 0,
    buyers_last_5m          INT         NOT NULL DEFAULT 0,
    buyer_acceleration      NUMERIC     NOT NULL DEFAULT 0,
    age_seconds             NUMERIC     NOT NULL DEFAULT 0,
    -- Adversarial indicators
    top_wallet_buy_share_5m NUMERIC     NOT NULL DEFAULT 0,
    wallet_diversity_ratio  NUMERIC     NOT NULL DEFAULT 0,
    repeat_buyer_share_1m   NUMERIC     NOT NULL DEFAULT 0,
    buy_sol_last_1m         NUMERIC     NOT NULL DEFAULT 0,
    sell_sol_last_1m        NUMERIC     NOT NULL DEFAULT 0,
    -- Clustering
    effective_buyers_1m         INT     NOT NULL DEFAULT 0,
    effective_buyers_5m         INT     NOT NULL DEFAULT 0,
    clustered_buyer_count       INT     NOT NULL DEFAULT 0,
    funding_cluster_ratio       NUMERIC NOT NULL DEFAULT 0,
    cluster_compression_1m      NUMERIC NOT NULL DEFAULT 0,
    cluster_compression_5m      NUMERIC NOT NULL DEFAULT 0,
    clustering_status           TEXT    NOT NULL DEFAULT 'unknown',
    clustering_backend          TEXT    NOT NULL DEFAULT 'null'
);

CREATE INDEX IF NOT EXISTS token_snapshots_mint_at_idx ON token_snapshots (mint, snapshot_at DESC);

-- ============================================================
-- Signals — every BUY/READY decision emitted by the live engine
-- ============================================================

CREATE TABLE IF NOT EXISTS signals (
    id                  BIGSERIAL   PRIMARY KEY,
    mint                TEXT        NOT NULL REFERENCES tokens(mint),
    emitted_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Decision
    label               TEXT        NOT NULL,   -- BUY | READY | WATCH | AVOID
    signal_state        TEXT        NOT NULL,   -- fresh | stale | expired
    is_actionable       BOOLEAN     NOT NULL DEFAULT FALSE,
    confidence_score    NUMERIC     NOT NULL DEFAULT 0,
    warming_up          BOOLEAN     NOT NULL DEFAULT FALSE,
    -- Execution context
    trade_size_sol      NUMERIC     NOT NULL DEFAULT 0,
    liquidity_proxy_sol NUMERIC     NOT NULL DEFAULT 0,
    estimated_impact_pct NUMERIC    NOT NULL DEFAULT 0,
    execution_penalty   NUMERIC     NOT NULL DEFAULT 0,
    adversarial_score   NUMERIC     NOT NULL DEFAULT 0,
    -- Effective buyers
    effective_buyers_1m INT         NOT NULL DEFAULT 0,
    effective_buyers_5m INT         NOT NULL DEFAULT 0,
    funding_cluster_ratio NUMERIC   NOT NULL DEFAULT 0,
    -- Rationale
    why_now             TEXT        NOT NULL DEFAULT '',
    why_not_higher      TEXT        NOT NULL DEFAULT '',
    reasons             TEXT[]      NOT NULL DEFAULT '{}',
    -- 7-gate engine
    engine_layer0_reject  BOOLEAN   NOT NULL DEFAULT FALSE,
    engine_layer0_reason  TEXT      NOT NULL DEFAULT '',
    engine_max_label      TEXT      NOT NULL DEFAULT '',
    engine_gates_pass_count INT     NOT NULL DEFAULT 0,
    engine_score_cap      INT       NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS signals_mint_at_idx     ON signals (mint, emitted_at DESC);
CREATE INDEX IF NOT EXISTS signals_label_idx       ON signals (label);
CREATE INDEX IF NOT EXISTS signals_actionable_idx  ON signals (is_actionable, emitted_at DESC) WHERE is_actionable = TRUE;

-- ============================================================
-- Per-gate results for each signal (explainability log)
-- ============================================================

CREATE TABLE IF NOT EXISTS signal_gate_results (
    id          BIGSERIAL   PRIMARY KEY,
    signal_id   BIGINT      NOT NULL REFERENCES signals(id) ON DELETE CASCADE,
    gate_id     INT         NOT NULL,
    gate_name   TEXT        NOT NULL,
    passed      BOOLEAN     NOT NULL,
    skipped     BOOLEAN     NOT NULL DEFAULT FALSE,
    value       NUMERIC     NOT NULL DEFAULT 0,
    threshold   NUMERIC     NOT NULL DEFAULT 0,
    margin      NUMERIC     NOT NULL DEFAULT 0,
    reason      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS signal_gate_results_signal_idx ON signal_gate_results (signal_id);

-- ============================================================
-- Realized outcomes — filled in after the fact for backtesting
-- ============================================================

CREATE TABLE IF NOT EXISTS realized_outcomes (
    id                   BIGSERIAL   PRIMARY KEY,
    signal_id            BIGINT      NOT NULL REFERENCES signals(id),
    mint                 TEXT        NOT NULL,
    outcome_recorded_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Price at signal vs outcome
    price_at_signal_sol  NUMERIC,
    price_peak_sol       NUMERIC,
    price_at_30m_sol     NUMERIC,
    price_at_60m_sol     NUMERIC,
    -- Return multiples
    mfe_30m              NUMERIC,   -- max favorable excursion at 30m
    realized_return_pct  NUMERIC,   -- realised P&L pct if exited at 30m
    -- Outcome label derived post-hoc
    outcome_label        TEXT,      -- winner | loser | stale | rug
    notes                TEXT
);

CREATE INDEX IF NOT EXISTS realized_outcomes_signal_idx ON realized_outcomes (signal_id);
CREATE INDEX IF NOT EXISTS realized_outcomes_mint_idx   ON realized_outcomes (mint);

-- ============================================================
-- Holder / funder clustering audit trail
-- ============================================================

CREATE TABLE IF NOT EXISTS clustering_audit (
    id            BIGSERIAL   PRIMARY KEY,
    mint          TEXT        NOT NULL,
    audited_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw_buyers    INT         NOT NULL DEFAULT 0,
    effective_buyers INT      NOT NULL DEFAULT 0,
    clustered_count  INT      NOT NULL DEFAULT 0,
    cluster_ratio    NUMERIC  NOT NULL DEFAULT 0,
    backend          TEXT     NOT NULL DEFAULT 'null'
);

CREATE INDEX IF NOT EXISTS clustering_audit_mint_idx ON clustering_audit (mint, audited_at DESC);

-- ============================================================
-- Swap events (raw, deduped by signature)
-- ============================================================

CREATE TABLE IF NOT EXISTS swap_events (
    signature    TEXT        PRIMARY KEY,
    slot         BIGINT      NOT NULL DEFAULT 0,
    block_time   TIMESTAMPTZ NOT NULL,
    mint         TEXT        NOT NULL,
    is_buy       BOOLEAN     NOT NULL,
    wallet_addr  TEXT        NOT NULL,
    sol_amount   NUMERIC     NOT NULL DEFAULT 0,
    token_amount NUMERIC     NOT NULL DEFAULT 0,
    program_id   TEXT        NOT NULL DEFAULT '',
    ingested_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS swap_events_mint_idx      ON swap_events (mint, block_time DESC);
CREATE INDEX IF NOT EXISTS swap_events_wallet_idx    ON swap_events (wallet_addr);
