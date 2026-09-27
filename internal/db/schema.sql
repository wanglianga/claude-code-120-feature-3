-- 海鲜预制菜工厂 批次解冻加工与召回服务 schema

CREATE TABLE IF NOT EXISTS users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  full_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('admin','manager','quality','warehouse','production','finance')),
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS suppliers (
  id BIGSERIAL PRIMARY KEY,
  code TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  contact TEXT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended'))
);

CREATE TABLE IF NOT EXISTS raw_material_batches (
  id BIGSERIAL PRIMARY KEY,
  batch_no TEXT UNIQUE NOT NULL,
  supplier_id BIGINT REFERENCES suppliers(id),
  species TEXT NOT NULL CHECK (species IN ('虾仁','扇贝','鱿鱼','鱼柳')),
  catch_or_production_date DATE NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  inbound_temp_c NUMERIC(5,2) NOT NULL,
  quarantine_cert TEXT NOT NULL,
  box_no TEXT NOT NULL,
  location TEXT NOT NULL,
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'in_storage'
    CHECK (status IN ('in_storage','locked','consumed','recalled','destroyed')),
  created_by BIGINT REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_raw_supplier ON raw_material_batches(supplier_id);

CREATE TABLE IF NOT EXISTS temp_logs (
  id BIGSERIAL PRIMARY KEY,
  raw_batch_id BIGINT NOT NULL REFERENCES raw_material_batches(id) ON DELETE CASCADE,
  temp_c NUMERIC(5,2) NOT NULL,
  recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  operator TEXT,
  note TEXT,
  alarm BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_templog_raw ON temp_logs(raw_batch_id);

CREATE TABLE IF NOT EXISTS production_lines (
  id BIGSERIAL PRIMARY KEY,
  code TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  active BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS seasoning_batches (
  id BIGSERIAL PRIMARY KEY,
  batch_no TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  supplier TEXT,
  expiry_date DATE
);

CREATE TABLE IF NOT EXISTS production_plans (
  id BIGSERIAL PRIMARY KEY,
  plan_no TEXT UNIQUE NOT NULL,
  product_name TEXT NOT NULL,
  line_id BIGINT REFERENCES production_lines(id),
  target_qty_kg NUMERIC(10,2),
  packaging_spec TEXT,                 -- 渠道定制包装要求
  required_seasoning_batch TEXT,       -- 计划指定调味料批次
  status TEXT NOT NULL DEFAULT 'planned'
    CHECK (status IN ('planned','thawing','processing','finished','cancelled')),
  planned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by BIGINT REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS thaw_records (
  id BIGSERIAL PRIMARY KEY,
  plan_id BIGINT NOT NULL REFERENCES production_plans(id) ON DELETE CASCADE,
  raw_batch_id BIGINT REFERENCES raw_material_batches(id),
  start_at TIMESTAMPTZ NOT NULL,
  end_at TIMESTAMPTZ NOT NULL,
  center_temp_c NUMERIC(5,2),
  water_batch_no TEXT,
  operator TEXT,
  duration_minutes INT GENERATED ALWAYS AS
    (CAST(EXTRACT(EPOCH FROM (end_at - start_at)) / 60 AS INT)) STORED,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_thaw_plan ON thaw_records(plan_id);

CREATE TABLE IF NOT EXISTS process_records (
  id BIGSERIAL PRIMARY KEY,
  plan_id BIGINT NOT NULL REFERENCES production_plans(id) ON DELETE CASCADE,
  stage TEXT NOT NULL CHECK (stage IN ('wash','trim','season','weigh','package','metalcheck','coldstorage')),
  operator TEXT,
  value_num NUMERIC(12,2),
  value_text TEXT,
  ok BOOLEAN NOT NULL DEFAULT true,
  note TEXT,
  recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_process_plan ON process_records(plan_id);

CREATE TABLE IF NOT EXISTS finished_batches (
  id BIGSERIAL PRIMARY KEY,
  batch_no TEXT UNIQUE NOT NULL,
  plan_id BIGINT REFERENCES production_plans(id),
  product_name TEXT NOT NULL,
  packaging_spec TEXT,
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,
  cold_storage_location TEXT,
  status TEXT NOT NULL DEFAULT 'in_stock'
    CHECK (status IN ('in_stock','released','recalled','quarantined','destroyed','reworked')),
  produced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by BIGINT REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_finished_plan ON finished_batches(plan_id);

CREATE TABLE IF NOT EXISTS batch_links (
  id BIGSERIAL PRIMARY KEY,
  finished_batch_id BIGINT NOT NULL REFERENCES finished_batches(id) ON DELETE CASCADE,
  raw_batch_id BIGINT NOT NULL REFERENCES raw_material_batches(id),
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,
  UNIQUE (finished_batch_id, raw_batch_id)
);
CREATE INDEX IF NOT EXISTS idx_links_raw ON batch_links(raw_batch_id);
CREATE INDEX IF NOT EXISTS idx_links_fin ON batch_links(finished_batch_id);

CREATE TABLE IF NOT EXISTS samples (
  id BIGSERIAL PRIMARY KEY,
  sample_no TEXT UNIQUE NOT NULL,
  finished_batch_id BIGINT NOT NULL REFERENCES finished_batches(id) ON DELETE CASCADE,
  taken_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  result TEXT NOT NULL DEFAULT 'pending' CHECK (result IN ('pending','qualified','unqualified')),
  tested_at TIMESTAMPTZ,
  lab_note TEXT,
  inspector TEXT
);
CREATE INDEX IF NOT EXISTS idx_samples_fin ON samples(finished_batch_id);

CREATE TABLE IF NOT EXISTS channels (
  id BIGSERIAL PRIMARY KEY,
  code TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  ctype TEXT NOT NULL CHECK (ctype IN ('商超','电商','直播','经销'))
);

CREATE TABLE IF NOT EXISTS shipments (
  id BIGSERIAL PRIMARY KEY,
  ship_no TEXT UNIQUE NOT NULL,
  channel_id BIGINT REFERENCES channels(id),
  shipped_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  status TEXT NOT NULL DEFAULT 'shipped' CHECK (status IN ('shipped','recalled','closed'))
);
CREATE INDEX IF NOT EXISTS idx_ship_channel ON shipments(channel_id);

CREATE TABLE IF NOT EXISTS shipment_items (
  id BIGSERIAL PRIMARY KEY,
  shipment_id BIGINT NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  boxes INT NOT NULL DEFAULT 0,
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_si_ship ON shipment_items(shipment_id);
CREATE INDEX IF NOT EXISTS idx_si_fin ON shipment_items(finished_batch_id);

CREATE TABLE IF NOT EXISTS orders (
  id BIGSERIAL PRIMARY KEY,
  order_no TEXT UNIQUE NOT NULL,
  shipment_id BIGINT REFERENCES shipments(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  channel_id BIGINT REFERENCES channels(id),
  box_no TEXT NOT NULL,
  qty NUMERIC(10,2) NOT NULL DEFAULT 0,
  customer TEXT,
  status TEXT NOT NULL DEFAULT 'sold' CHECK (status IN ('sold','recalled','returned','compensated')),
  sold_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_orders_fin ON orders(finished_batch_id);
CREATE INDEX IF NOT EXISTS idx_orders_ship ON orders(shipment_id);

CREATE TABLE IF NOT EXISTS channel_feedback (
  id BIGSERIAL PRIMARY KEY,
  channel_id BIGINT REFERENCES channels(id),
  order_id BIGINT REFERENCES orders(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  ftype TEXT NOT NULL,
  content TEXT,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  handled BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS quality_events (
  id BIGSERIAL PRIMARY KEY,
  event_no TEXT UNIQUE NOT NULL,
  etype TEXT NOT NULL CHECK (etype IN
    ('temp_exceed','thaw_timeout','seasoning_mismatch','metal_alarm','package_leak',
     'channel_odor','live_return','regulatory_recall')),
  severity TEXT NOT NULL DEFAULT 'major' CHECK (severity IN ('minor','major','critical')),
  raw_batch_id BIGINT REFERENCES raw_material_batches(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  order_id BIGINT REFERENCES orders(id),
  description TEXT,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','recalled','closed')),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_events_raw ON quality_events(raw_batch_id);
CREATE INDEX IF NOT EXISTS idx_events_fin ON quality_events(finished_batch_id);

CREATE TABLE IF NOT EXISTS recalls (
  id BIGSERIAL PRIMARY KEY,
  recall_no TEXT UNIQUE NOT NULL,
  event_id BIGINT REFERENCES quality_events(id),
  level TEXT NOT NULL DEFAULT 'class2' CHECK (level IN ('class1','class2','class3')),
  reason TEXT,
  status TEXT NOT NULL DEFAULT 'executing' CHECK (status IN ('executing','closed')),
  initiated_by BIGINT REFERENCES users(id),
  initiated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS recall_actions (
  id BIGSERIAL PRIMARY KEY,
  recall_id BIGINT NOT NULL REFERENCES recalls(id) ON DELETE CASCADE,
  atype TEXT NOT NULL CHECK (atype IN ('stop_sale','return','compensation','supplier_claim')),
  doc_no TEXT NOT NULL,
  title TEXT,
  content TEXT,
  amount NUMERIC(12,2) NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'issued',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_actions_recall ON recall_actions(recall_id);

CREATE TABLE IF NOT EXISTS return_goods (
  id BIGSERIAL PRIMARY KEY,
  return_no TEXT UNIQUE NOT NULL,
  recall_id BIGINT REFERENCES recalls(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  box_no TEXT,
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,
  arrived_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  disposition TEXT CHECK (disposition IN ('reinspect','rework','destroy')),
  decided_by TEXT,
  decided_at TIMESTAMPTZ,
  note TEXT
);
CREATE INDEX IF NOT EXISTS idx_return_fin ON return_goods(finished_batch_id);

CREATE TABLE IF NOT EXISTS inventory (
  id BIGSERIAL PRIMARY KEY,
  finished_batch_id BIGINT NOT NULL REFERENCES finished_batches(id),
  location TEXT NOT NULL,
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'available'
    CHECK (status IN ('available','quarantined','reworked','destroyed')),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (finished_batch_id, location, status)
);
CREATE INDEX IF NOT EXISTS idx_inv_fin ON inventory(finished_batch_id);

CREATE TABLE IF NOT EXISTS stocktakes (
  id BIGSERIAL PRIMARY KEY,
  stocktake_no TEXT UNIQUE NOT NULL,
  location TEXT,
  counted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  operator TEXT,
  note TEXT
);

CREATE TABLE IF NOT EXISTS stocktake_items (
  id BIGSERIAL PRIMARY KEY,
  stocktake_id BIGINT NOT NULL REFERENCES stocktakes(id) ON DELETE CASCADE,
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  system_qty NUMERIC(10,2),
  counted_qty NUMERIC(10,2),
  diff_qty NUMERIC(10,2)
);

CREATE TABLE IF NOT EXISTS supplier_deductions (
  id BIGSERIAL PRIMARY KEY,
  deduction_no TEXT UNIQUE NOT NULL,
  supplier_id BIGINT REFERENCES suppliers(id),
  recall_id BIGINT REFERENCES recalls(id),
  raw_batch_id BIGINT REFERENCES raw_material_batches(id),
  amount NUMERIC(12,2) NOT NULL,
  reason TEXT,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','confirmed','deducted')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS customer_compensations (
  id BIGSERIAL PRIMARY KEY,
  comp_no TEXT UNIQUE NOT NULL,
  order_id BIGINT REFERENCES orders(id),
  amount NUMERIC(12,2) NOT NULL,
  reason TEXT,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS regulatory_samplings (
  id BIGSERIAL PRIMARY KEY,
  sampling_no TEXT UNIQUE NOT NULL,
  regulator TEXT NOT NULL,
  target_type TEXT NOT NULL CHECK (target_type IN ('raw','finished')),
  raw_batch_id BIGINT REFERENCES raw_material_batches(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  sampled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  result TEXT NOT NULL DEFAULT 'pending' CHECK (result IN ('pending','qualified','unqualified')),
  note TEXT
);

-- ================= 直播间集中退货 =================

-- 直播场次：主播承诺（时效/文案）是复盘对比的基准
CREATE TABLE IF NOT EXISTS live_sessions (
  id BIGSERIAL PRIMARY KEY,
  session_no TEXT UNIQUE NOT NULL,
  channel_id BIGINT REFERENCES channels(id),
  anchor TEXT NOT NULL,                            -- 主播
  title TEXT NOT NULL,                             -- 场次主题
  live_at TIMESTAMPTZ NOT NULL,                    -- 开播时间
  promised_delivery_hours INT NOT NULL DEFAULT 48, -- 主播承诺到货时效（小时）
  promise_text TEXT,                               -- 主播承诺文案（规格/赠品/坏单包赔等）
  status TEXT NOT NULL DEFAULT 'live'
    CHECK (status IN ('live','returning','reviewed')),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_live_session_channel ON live_sessions(channel_id);

-- 一场直播针对某成品批次的集中退货（退货批次）
CREATE TABLE IF NOT EXISTS live_return_batches (
  id BIGSERIAL PRIMARY KEY,
  batch_no TEXT UNIQUE NOT NULL,
  session_id BIGINT NOT NULL REFERENCES live_sessions(id),
  finished_batch_id BIGINT REFERENCES finished_batches(id),
  opened_by BIGINT REFERENCES users(id),
  opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at TIMESTAMPTZ,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
  note TEXT
);
CREATE INDEX IF NOT EXISTS idx_lrb_session ON live_return_batches(session_id);

-- 集中退货登记：四类原因（主播承诺不符/物流延迟/口味问题/批次质量异常）
CREATE TABLE IF NOT EXISTS live_return_orders (
  id BIGSERIAL PRIMARY KEY,
  entry_no TEXT UNIQUE NOT NULL,
  batch_id BIGINT NOT NULL REFERENCES live_return_batches(id) ON DELETE CASCADE,
  order_id BIGINT REFERENCES orders(id),
  reason_cat TEXT NOT NULL CHECK (reason_cat IN
    ('promise_mismatch','logistics_delay','taste_problem','batch_quality')),
  reason_detail TEXT,
  qty NUMERIC(10,2) NOT NULL DEFAULT 1,            -- 退货数量（盒）
  qty_kg NUMERIC(10,2) NOT NULL DEFAULT 0,         -- 退回重量（库存退回用）
  amount NUMERIC(12,2) NOT NULL DEFAULT 0,         -- 退款额
  promised_delivery_hours INT,                     -- 快照：主播承诺时效
  actual_delivery_hours NUMERIC(8,1),              -- 实际到货时效（小时）
  sample_result TEXT,                              -- 快照：批次留样/复检结论
  registered_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lro_batch ON live_return_orders(batch_id);

-- 每笔退货分别生成四项处置：退款 / 库存退回 / 客服话术 / 渠道扣款
CREATE TABLE IF NOT EXISTS live_return_disposals (
  id BIGSERIAL PRIMARY KEY,
  doc_no TEXT UNIQUE NOT NULL,
  entry_id BIGINT NOT NULL REFERENCES live_return_orders(id) ON DELETE CASCADE,
  dtype TEXT NOT NULL CHECK (dtype IN ('refund','restock','script','channel_deduction')),
  title TEXT,
  content TEXT,
  amount NUMERIC(12,2) NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','done')),
  executed_by TEXT,
  executed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (entry_id, dtype)
);
CREATE INDEX IF NOT EXISTS idx_lrd_entry ON live_return_disposals(entry_id);

-- 渠道复盘：对比主播承诺 / 实际到货时效 / 批次检测，形成建议并同步渠道
CREATE TABLE IF NOT EXISTS channel_reviews (
  id BIGSERIAL PRIMARY KEY,
  review_no TEXT UNIQUE NOT NULL,
  batch_id BIGINT NOT NULL REFERENCES live_return_batches(id),
  channel_id BIGINT REFERENCES channels(id),
  promise_summary TEXT,                            -- 对比①：主播承诺
  delivery_summary TEXT,                           -- 对比②：实际到货时效
  sample_summary TEXT,                             -- 对比③：批次检测
  suggestion TEXT NOT NULL CHECK (suggestion IN
    ('channel_deduction','quality_rectification','logistics_claim','none')),
  suggestion_detail TEXT,
  refund_total NUMERIC(12,2) NOT NULL DEFAULT 0,   -- 退款合计
  deduction_amount NUMERIC(12,2) NOT NULL DEFAULT 0, -- 渠道扣款合计
  restock_adjustment TEXT,                         -- 对下一场直播备货的影响
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','synced')),
  reviewed_by BIGINT REFERENCES users(id),
  reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  synced_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_review_batch ON channel_reviews(batch_id);
CREATE INDEX IF NOT EXISTS idx_review_channel ON channel_reviews(channel_id);

-- 复盘同步后形成的「下一场直播备货」建议（按成品批次保留最新一条）
CREATE TABLE IF NOT EXISTS live_restock_advice (
  id BIGSERIAL PRIMARY KEY,
  finished_batch_id BIGINT NOT NULL REFERENCES finished_batches(id),
  session_id BIGINT REFERENCES live_sessions(id),
  review_id BIGINT REFERENCES channel_reviews(id),
  direction TEXT NOT NULL CHECK (direction IN ('down','hold','switch_batch','pause')),
  adjust_pct INT NOT NULL DEFAULT 0,            -- 备货量调整百分比（下调）
  advice TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (finished_batch_id)
);

-- 库存退回执行后挂接既有退货三态处置（可复检/可返工/必须销毁）
ALTER TABLE return_goods ADD COLUMN IF NOT EXISTS live_entry_id BIGINT REFERENCES live_return_orders(id);
