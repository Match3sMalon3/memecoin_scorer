-- Signal snapshots: one row per classification decision (deduped per 5-min bucket).
--
-- Return convention (applies to signal_outcomes below):
--   Returns are DECIMAL, not multiples.
--     0.20 = +20% = 1.2x
--     1.00 = +100% = 2x
--     return_vs_signal  = price_at_checkpoint / price_at_signal - 1
--     max_return_so_far = max_price_in_window  / price_at_signal - 1

CREATE TABLE IF NOT EXISTS signal_snapshots (
  id                      BIGSERIAL PRIMARY KEY,
  mint                    TEXT NOT NULL,
  signaled_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  bucket_5m               TIMESTAMPTZ NOT NULL,
  classification_version  TEXT NOT NULL,

  setup_mode              TEXT NOT NULL,
  blocker_severity        TEXT NOT NULL,
  action                  TEXT NOT NULL,
  blockers                JSONB NOT NULL,
  blocker_signature       TEXT NOT NULL,
  proxy_score             NUMERIC,

  age_at_signal_s         INT,
  holder_count            INT,
  top10_pct               NUMERIC,
  clustering_mode         TEXT,
  catalyst                TEXT,

  real_depth_sol          NUMERIC,
  liq_source              TEXT,
  sol_per_trade_5m        NUMERIC,

  price_sol_at_signal     NUMERIC,
  price_source            TEXT NOT NULL,   -- raydium_reserves | observed_trade | unavailable
  price_reliable          BOOLEAN NOT NULL,
  pool_depth_at_signal    NUMERIC,

  raw_snapshot            JSONB
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_signal_snapshot
  ON signal_snapshots (mint, setup_mode, blocker_signature, bucket_5m, classification_version);
CREATE INDEX IF NOT EXISTS idx_signal_snapshots_signaled_at ON signal_snapshots (signaled_at);
CREATE INDEX IF NOT EXISTS idx_signal_snapshots_mint        ON signal_snapshots (mint, signaled_at);

CREATE TABLE IF NOT EXISTS signal_outcomes (
  signal_id           BIGINT NOT NULL REFERENCES signal_snapshots(id) ON DELETE CASCADE,
  checkpoint_s        INT NOT NULL CHECK (checkpoint_s IN (300, 900, 1800)),
  measured_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

  price_sol           NUMERIC,
  price_source        TEXT,
  price_reliable      BOOLEAN,
  pool_depth_sol      NUMERIC,

  return_vs_signal    NUMERIC,
  max_return_so_far   NUMERIC,

  is_tradeable        BOOLEAN,
  is_1_2x_clean       BOOLEAN,
  is_2x_clean         BOOLEAN,
  is_rugged           BOOLEAN,

  measurement_status  TEXT NOT NULL DEFAULT 'measured',  -- measured | unavailable | error
  notes               TEXT,

  PRIMARY KEY (signal_id, checkpoint_s)
);
CREATE INDEX IF NOT EXISTS idx_signal_outcomes_status ON signal_outcomes (measurement_status, checkpoint_s);

CREATE OR REPLACE VIEW v_outcomes_summary AS
SELECT
  s.classification_version,
  s.setup_mode,
  s.action,
  s.liq_source,
  s.blocker_signature,
  o.checkpoint_s,
  COUNT(*)                                              AS n_completed,
  COUNT(*) FILTER (WHERE o.is_tradeable)                AS n_tradeable,
  COUNT(*) FILTER (WHERE o.is_1_2x_clean)               AS n_1_2x_clean,
  COUNT(*) FILTER (WHERE o.is_2x_clean)                 AS n_2x_clean,
  COUNT(*) FILTER (WHERE o.is_rugged)                   AS n_rugged,
  AVG(o.return_vs_signal)                               AS avg_return,
  AVG(o.max_return_so_far)                              AS avg_max_return,
  PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY o.return_vs_signal)  AS median_return,
  PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY o.max_return_so_far) AS median_max_return
FROM signal_snapshots s
JOIN signal_outcomes  o ON o.signal_id = s.id
WHERE o.measurement_status = 'measured'
GROUP BY 1,2,3,4,5,6;

CREATE OR REPLACE VIEW v_outcomes_pending AS
SELECT s.classification_version, s.setup_mode, COUNT(*) AS n_pending
FROM signal_snapshots s
LEFT JOIN signal_outcomes o
  ON o.signal_id = s.id AND o.checkpoint_s = 1800
WHERE o.signal_id IS NULL
  AND s.signaled_at + interval '30 minutes' > now()
GROUP BY 1,2;
