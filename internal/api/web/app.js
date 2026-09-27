/* 海鲜预制菜批次追溯与召回系统 SPA */
'use strict';

const state = { user: null, tab: 'dashboard', cache: {} };

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];

async function api(path, opts = {}) {
  const res = await fetch('/api' + path, {
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    ...opts,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  let data = null;
  try { data = await res.json(); } catch (_) {}
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}

function toast(msg, type = '') {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast ' + type;
  setTimeout(() => t.classList.add('hidden'), 2600);
}

function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}
function fmtNum(v, d = 2) { return v === null || v === undefined ? '' : Number(v).toFixed(d); }

const STATUS_TAG = {
  in_storage: ['gray', '在库'], locked: ['amber', '锁定待判'], consumed: ['gray', '已耗用'],
  recalled: ['red', '召回'], destroyed: ['red', '已销毁'],
  in_stock: ['green', '成品在库'], released: ['blue', '已放行'], quarantined: ['amber', '隔离'],
  reworked: ['purple', '返工'], available: ['green', '可用'],
  open: ['amber', '待处置'], executing: ['red', '执行中'], closed: ['gray', '已结案'],
  sold: ['blue', '已售'], returned: ['amber', '已退货'], compensated: ['green', '已赔付'],
  shipped: ['blue', '已发货'], pending: ['amber', '待处理'], paid: ['green', '已赔付'],
  confirmed: ['blue', '已确认'], deducted: ['green', '已扣款'], refunded: ['green', '已退款'],
  qualified: ['green', '合格'], unqualified: ['red', '不合格'],
  registered: ['amber', '待复盘'], reviewed: ['blue', '已复盘'], synced: ['purple', '渠道已同步'],
};
const ETYPE = {
  temp_exceed: '原料温度超标', thaw_timeout: '解冻超时', seasoning_mismatch: '调味料批次用错',
  metal_alarm: '金检报警', package_leak: '包装漏气', channel_odor: '渠道反馈异味',
  live_return: '直播间集中退货', regulatory_recall: '监管要求召回',
};
const STAGE = { wash: '清洗', trim: '切配', season: '调味', weigh: '称重', package: '包装', metalcheck: '金检', coldstorage: '成品冷库' };
const DISP = { reinspect: ['amber', '可复检'], rework: ['purple', '可返工'], destroy: ['red', '必须销毁'] };
const LREASON = {
  promise_mismatch: ['red', '主播承诺不符'],
  logistics_delay: ['amber', '物流延迟'],
  taste_issue: ['purple', '口味问题'],
  batch_quality: ['red', '批次质量异常'],
};
const CHARGE_TYPE = {
  commitment_mismatch: '主播承诺不符扣款', logistics_breach: '物流时效违约扣款',
  quality_penalty: '批次质量违约金', service_share: '客诉服务分担扣款',
};
const SUG_TYPE = {
  channel_deduction: ['red', '渠道扣款'], quality_rectification: ['amber', '质量整改'],
  logistics_accountability: ['blue', '物流追责'],
};
const can = (...roles) => roles.includes(state.user.role) || state.user.role === 'admin';

function tag(status) {
  const t = STATUS_TAG[status];
  if (!t) return `<span class="tag gray">${esc(status)}</span>`;
  return `<span class="tag ${t[0]}">${t[1]}</span>`;
}

/* ---------- 登录 / 框架 ---------- */

async function boot() {
  try {
    state.user = await api('/auth/me');
    enterApp();
  } catch (_) {
    $('#login-view').classList.remove('hidden');
  }
}

$('#login-form').addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(e.target);
  try {
    state.user = await api('/auth/login', { method: 'POST', body: { username: fd.get('username'), password: fd.get('password') } });
    enterApp();
  } catch (err) {
    $('#login-err').textContent = err.message;
  }
});

$('#logout-btn').addEventListener('click', async () => {
  await api('/auth/logout', { method: 'POST' });
  location.reload();
});

const TABS = [
  ['dashboard', '仪表盘'], ['raw', '原料入库'], ['production', '生产加工'],
  ['finished', '成品批次'], ['channel', '渠道履约'], ['events', '品控事件'],
  ['live', '直播退货'], ['recalls', '召回管理'], ['returns', '退货处置'], ['inventory', '冷库库存'],
  ['finance', '财务追偿'], ['sampling', '监管抽样'],
];

function enterApp() {
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  $('#user-label').textContent = `${state.user.full_name}（${roleName(state.user.role)}）`;
  $('#tabs').innerHTML = TABS.map(([k, n]) => `<button data-tab="${k}">${n}</button>`).join('');
  $$('#tabs button').forEach(b => b.onclick = () => { state.tab = b.dataset.tab; render(); });
  render();
}
function roleName(r) {
  return { admin: '管理员', manager: '经理', quality: '品控', warehouse: '仓管', production: '生产', finance: '财务' }[r] || r;
}

function render() {
  $$('#tabs button').forEach(b => b.classList.toggle('active', b.dataset.tab === state.tab));
  const pages = {
    dashboard: pageDashboard, raw: pageRaw, production: pageProduction,
    finished: pageFinished, channel: pageChannel, events: pageEvents,
    live: pageLive, recalls: pageRecalls, returns: pageReturns, inventory: pageInventory,
    finance: pageFinance, sampling: pageSampling,
  };
  $('#content').innerHTML = '';
  pages[state.tab]();
}

function pageHead(title, sub, actions = '') {
  return `<div class="page-head"><div><h2>${title}</h2><div class="sub">${sub}</div></div><div class="toolbar">${actions}</div></div>`;
}

function table(columns, rows) {
  if (!rows.length) return '<div class="flow-note">暂无数据</div>';
  const head = columns.map(c => `<th>${c.t}</th>`).join('');
  const body = rows.map(r => '<tr>' + columns.map(c => {
    let v = c.fn ? c.fn(r) : r[c.k];
    if (c.tag) v = tag(r[c.k]);
    return `<td class="${c.cls || ''}">${v === null || v === undefined || v === '' ? '—' : v}</td>`;
  }).join('') + '</tr>').join('');
  return `<div class="table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`;
}

/* ---------- 通用弹窗表单 ---------- */

function closeModal() { $('#modal').classList.add('hidden'); }
$('#modal-close').onclick = closeModal;
$('#modal').addEventListener('click', e => { if (e.target.id === 'modal') closeModal(); });

function modalForm(title, fields, onSubmit, submitText = '提交') {
  $('#modal-title').textContent = title;
  $('#modal-body').innerHTML = `
    <form id="mform" class="form-grid">
      ${fields.map(f => {
        const full = f.full ? 'full' : '';
        if (f.type === 'textarea')
          return `<label class="${full}">${f.label}<textarea name="${f.name}" rows="3" placeholder="${f.ph || ''}">${esc(f.def || '')}</textarea></label>`;
        if (f.type === 'select')
          return `<label class="${full}">${f.label}<select name="${f.name}">${(f.opts || []).map(o =>
            `<option value="${esc(o.v)}" ${o.v === f.def ? 'selected' : ''}>${esc(o.t)}</option>`).join('')}</select></label>`;
        return `<label class="${full}">${f.label}<input name="${f.name}" type="${f.type || 'text'}"
          value="${esc(f.def || '')}" placeholder="${f.ph || ''}" ${f.step ? 'step="' + f.step + '"' : ''}></label>`;
      }).join('')}
      <div class="full form-actions">
        <button type="button" class="act gray" id="mcancel">取消</button>
        <button type="submit" class="act">${submitText}</button>
      </div>
    </form>`;
  $('#modal').classList.remove('hidden');
  $('#mcancel').onclick = closeModal;
  $('#mform').addEventListener('submit', async e => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const body = {};
    fields.forEach(f => {
      let v = fd.get(f.name);
      if (f.type === 'number') v = v === '' ? null : Number(v);
      body[f.name] = v;
    });
    try {
      await onSubmit(body);
      closeModal();
      render();
    } catch (err) { toast(err.message, 'err'); }
  });
}

function modalView(title, html) {
  $('#modal-title').textContent = title;
  $('#modal-body').innerHTML = html;
  $('#modal').classList.remove('hidden');
}

/* ---------- 仪表盘 ---------- */

async function pageDashboard() {
  const d = await api('/dashboard');
  const c = $('#content');
  const kpi = (v, l, cls = '') => `<div class="kpi ${cls}"><div class="v">${v}</div><div class="l">${l}</div></div>`;
  c.innerHTML = pageHead('工厂运行总览', '采购入库 → 解冻加工 → 成品留样 → 渠道发货 → 召回追偿 全链一览') +
    `<div class="cards">
      ${kpi(d.raw_total, '原料批次总数')}
      ${kpi(d.raw_locked, '原料锁定待判', d.raw_locked ? 'warn' : 'ok')}
      ${kpi(d.finished_total, '成品批次总数')}
      ${kpi(d.stock_kg + ' kg', '成品可用库存')}
      ${kpi(d.quarantine_kg + ' kg', '召回隔离库存', d.quarantine_kg ? 'danger' : '')}
      ${kpi(d.events_open, '待处置品控事件', d.events_open ? 'warn' : 'ok')}
      ${kpi(d.recalls_active, '执行中召回', d.recalls_active ? 'danger' : 'ok')}
      ${kpi(d.returns_pending, '退货待判定', d.returns_pending ? 'warn' : 'ok')}
      ${kpi(d.live_returns_pending_review, '直播退货待复盘', d.live_returns_pending_review ? 'danger' : 'ok')}
      ${kpi(d.live_charges_pending, '直播渠道扣款待落实', d.live_charges_pending ? 'warn' : 'ok')}
      ${kpi(d.comp_pending, '待支付赔付', d.comp_pending ? 'warn' : 'ok')}
      ${kpi(d.deduction_pending, '待落实供应商扣款', d.deduction_pending ? 'warn' : 'ok')}
    </div>
    <div class="panel"><h3>建议演示路径</h3><div class="panel-body">
      <div class="flow-note">
        ① 在「原料入库」查看 RM20260903（入库温度 -15.1℃ 自动锁定）→ 点「正向追溯」看它流入成品 FG20260912-02；<br>
        ② 在「品控事件」对 <b>EV20260903-01</b> 发起召回，系统自动圈定成品、已发货渠道、在库库存、已售订单（精确到箱）与留样结果，并生成停售/退货/赔偿/供应商追责四类材料；<br>
        ③ 在「召回管理」打开召回单查看材料；「退货处置」登记退货并分别判定 可复检/可返工/必须销毁，观察库存状态变化；<br>
        ④ 「财务追偿」支付消费者赔付、确认供应商扣款；监管抽样不合格会自动生成监管召回事件。<br>
        ⑤ 「直播退货」打开 <b>LR20260916-01</b>：四类原因（承诺不符/物流延迟/口味/批次质量）已分别生成退款、库存退回、客服话术、渠道扣款；点「生成复盘」对比主播承诺·到货时效·批次检测，得出渠道扣款/质量整改/物流追责建议与下场备货调整，再「同步渠道」。
      </div>
    </div></div>`;
}

/* ---------- 原料入库 ---------- */

async function pageRaw() {
  const [rows, sups] = await Promise.all([api('/raw-batches'), api('/suppliers')]);
  state.cache.suppliers = sups;
  const acts = can('warehouse', 'manager')
    ? `<button class="act" id="new-raw">➕ 冻品入库登记</button>` : '';
  $('#content').innerHTML = pageHead('采购入库 · 冻品原料批次', '供应商 / 捕捞生产日期 / 入库温度 / 检疫证明 / 箱号 / 库位；入库温度高于 -18℃ 自动锁定', acts) +
    `<div class="panel">${table([
      { t: '原料批号', k: 'batch_no' },
      { t: '供应商', fn: r => `${esc(r.supplier_code || '')} ${esc(r.supplier_name || '')}` },
      { t: '品类', k: 'species' },
      { t: '捕捞/生产日期', k: 'catch_date' },
      { t: '入库温度℃', fn: r => { const hot = Number(r.inbound_temp_c) > -18; return `<b style="color:${hot ? '#b91c1c' : ''}">${fmtNum(r.inbound_temp_c)}</b>`; } },
      { t: '检疫证明', k: 'quarantine_cert' },
      { t: '箱号', k: 'box_no' },
      { t: '库位', k: 'location' },
      { t: '重量kg', fn: r => fmtNum(r.qty_kg) },
      { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => `<button class="act" data-trace="${esc(r.batch_no)}">正向追溯</button>` +
          (can('warehouse', 'manager') ? ` <button class="act amber" data-temp="${esc(r.batch_no)}">补测温</button>` : '') },
    ], rows)}</div>`;
  $('#new-raw')?.addEventListener('click', () => modalForm('冻品入库登记', [
    { name: 'batch_no', label: '原料批号 *', ph: 'RM20260920-01' },
    { name: 'supplier_code', label: '供应商', type: 'select', opts: sups.map(s => ({ v: s.code, t: s.code + ' ' + s.name })) },
    { name: 'species', label: '品类', type: 'select', opts: ['虾仁', '扇贝', '鱿鱼', '鱼柳'].map(x => ({ v: x, t: x })) },
    { name: 'catch_date', label: '捕捞/生产日期', ph: '2026-09-20' },
    { name: 'inbound_temp_c', label: '入库温度℃（限值 ≤ -18）', type: 'number', step: '0.01', def: '-18.00' },
    { name: 'qty_kg', label: '重量 kg', type: 'number', step: '0.01', def: '100' },
    { name: 'box_no', label: '箱号' }, { name: 'location', label: '库位' },
    { name: 'quarantine_cert', label: '检疫证明编号', full: true },
  ], async b => {
    const r = await api('/raw-batches', { method: 'POST', body: b });
    toast(r.temp_alarm ? '已登记：温度超标，批次自动锁定！' : '入库登记成功', r.temp_alarm ? 'err' : 'ok');
  }, '登记入库'));
  $$('button[data-temp]').forEach(btn => btn.onclick = () => modalForm('补测仓储温度 · ' + btn.dataset.temp, [
    { name: 'temp_c', label: '库温℃（高于 -18 自动锁定）', type: 'number', step: '0.01', def: '-18.00' },
    { name: 'operator', label: '操作人' },
    { name: 'note', label: '备注', type: 'textarea', full: true },
  ], async b => {
    const r = await api(`/raw-batches/${btn.dataset.temp}/temp-logs`, { method: 'POST', body: b });
    toast(r.alarm ? '温度超标，批次已锁定' : '测温已记录', r.alarm ? 'err' : 'ok');
  }, '提交测温'));
  $$('button[data-trace]').forEach(btn => btn.onclick = () => showRawTrace(btn.dataset.trace));
}

async function showRawTrace(bn) {
  const d = await api(`/raw-batches/${bn}/trace`);
  const r = d.raw;
  const kv = (k, v) => `<dt>${k}</dt><dd>${v}</dd>`;
  modalView(`正向追溯 · 原料批次 ${bn}`, `
    <div class="trace-grid">
      <div class="panel"><h3>原料档案</h3><div class="panel-body"><dl class="kv">
        ${kv('品类', esc(r.species))}${kv('供应商', esc(r.supplier_name))}
        ${kv('捕捞/生产', esc(r.catch_date))}${kv('入库温度', fmtNum(r.inbound_temp_c) + ' ℃')}
        ${kv('检疫证明', esc(r.quarantine_cert))}${kv('箱号', esc(r.box_no))}
        ${kv('库位', esc(r.location))}${kv('重量', fmtNum(r.qty_kg) + ' kg')}
        ${kv('状态', tag(r.status))}
      </dl></div></div>
      <div class="panel"><h3>仓储温控记录</h3><div class="panel-body">${table([
        { t: '温度℃', fn: x => `<span style="color:${x.alarm ? '#b91c1c' : ''}">${fmtNum(x.temp_c)}</span>` },
        { t: '时间', k: 'recorded_at' }, { t: '操作人', k: 'operator' },
        { t: '报警', fn: x => x.alarm ? '<span class="tag red">超标</span>' : '<span class="tag green">正常</span>' },
        { t: '备注', k: 'note', cls: 'wrap' },
      ], d.temp_logs)}</div></div>
      <div class="panel full"><h3>进入了哪些成品批次</h3><div class="panel-body">${table([
        { t: '成品批号', fn: x => `<button class="act" data-fin="${esc(x.batch_no)}">${esc(x.batch_no)}</button>` },
        { t: '产品', k: 'product_name' }, { t: '渠道定制包装', k: 'packaging_spec' },
        { t: '投料kg', fn: x => fmtNum(x.used_qty_kg) }, { t: '成品量kg', fn: x => fmtNum(x.qty_kg) },
        { t: '状态', k: 'status', tag: true },
      ], d.finished)}</div></div>
      <div class="panel"><h3>留样检测结果</h3><div class="panel-body">${table([
        { t: '成品', k: 'finished_batch_no' }, { t: '留样号', k: 'sample_no' },
        { t: '结果', k: 'result', tag: true }, { t: '检测时间', k: 'tested_at' },
        { t: '备注', k: 'lab_note', cls: 'wrap' },
      ], d.samples)}</div></div>
      <div class="panel"><h3>发往哪些渠道</h3><div class="panel-body">${table([
        { t: '发货单', k: 'ship_no' }, { t: '渠道', k: 'channel' }, { t: '类型', k: 'ctype' },
        { t: '成品', k: 'finished_batch_no' }, { t: '箱数', k: 'boxes' }, { t: '重量kg', fn: x => fmtNum(x.qty_kg) },
      ], d.shipments)}</div></div>
      <div class="panel full"><h3>已售订单（精确到箱）</h3><div class="panel-body">${table([
        { t: '订单号', k: 'order_no' }, { t: '渠道', k: 'channel' }, { t: '成品', k: 'finished_batch_no' },
        { t: '箱号', k: 'box_no' }, { t: '数量', k: 'qty' }, { t: '顾客', k: 'customer' },
        { t: '状态', k: 'status', tag: true }, { t: '售出时间', k: 'sold_at' },
      ], d.orders)}</div></div>
      <div class="panel"><h3>品控事件</h3><div class="panel-body">${table([
        { t: '事件号', k: 'event_no' }, { t: '类型', fn: x => ETYPE[x.etype] },
        { t: '等级', k: 'severity' }, { t: '状态', k: 'status', tag: true },
        { t: '描述', k: 'description', cls: 'wrap' },
      ], d.events)}</div></div>
      <div class="panel"><h3>召回记录</h3><div class="panel-body">${table([
        { t: '召回号', k: 'recall_no' }, { t: '级别', k: 'level' },
        { t: '状态', k: 'status', tag: true }, { t: '原因', k: 'reason', cls: 'wrap' },
      ], d.recalls)}</div></div>
    </div>`);
  $$('button[data-fin]').forEach(b => b.onclick = () => { closeModal(); showFinTrace(b.dataset.fin); });
}

/* ---------- 生产加工 ---------- */

async function pageProduction() {
  const [plans, lines, seas] = await Promise.all([api('/plans'), api('/lines'), api('/seasonings')]);
  state.cache.lines = lines; state.cache.seasonings = seas;
  const acts = can('production', 'manager') ? `<button class="act" id="new-plan">➕ 下发生产计划</button>` : '';
  $('#content').innerHTML = pageHead('生产计划 · 解冻间 · 加工线', '多条产线并行；解冻记录开始/结束时间、中心温度、用水批次、操作人；加工七步全程留痕', acts) +
    `<div class="panel"><div class="table-wrap"><table><thead><tr>
      <th>计划号</th><th>产品</th><th>产线</th><th>目标kg</th><th>渠道定制包装</th><th>指定调味料</th><th>状态</th><th>操作</th>
      </tr></thead><tbody>${plans.map(p => `<tr>
        <td>${esc(p.plan_no)}</td><td>${esc(p.product_name)}</td>
        <td>${esc(p.code)} ${esc(p.name)}</td><td>${fmtNum(p.target_qty_kg)}</td>
        <td>${esc(p.packaging_spec)}</td><td>${esc(p.required_seasoning_batch)}</td>
        <td>${tag(p.status === 'finished' ? 'closed' : p.status === 'planned' ? 'pending' : 'executing')}</td>
        <td>
          ${can('production', 'manager') ? `
          <button class="act amber" data-thaw="${p.id}">解冻记录</button>
          <button class="act" data-proc="${p.id}">加工记录</button>
          <button class="act green" data-finish="${p.id}" ${p.status === 'finished' ? 'disabled' : ''}>完工入冷库</button>` : ''}
        </td></tr>`).join('')}</tbody></table></div></div>`;
  $('#new-plan')?.addEventListener('click', () => modalForm('下发生产计划', [
    { name: 'plan_no', label: '计划号 *' }, { name: 'product_name', label: '产品名称 *' },
    { name: 'line_code', label: '生产线（多线并行）', type: 'select', opts: lines.map(l => ({ v: l.code, t: l.code + ' ' + l.name })) },
    { name: 'target_qty_kg', label: '目标产量 kg', type: 'number', step: '0.01', def: '100' },
    { name: 'required_seasoning_batch', label: '指定调味料批次', type: 'select', opts: seas.map(s => ({ v: s.batch_no, t: s.batch_no + ' ' + s.name })) },
    { name: 'packaging_spec', label: '渠道定制包装要求', full: true, ph: '如：直播间 250g 彩包充氮' },
  ], b => api('/plans', { method: 'POST', body: b }).then(() => toast('计划已下发', 'ok')), '下发'));

  $$('button[data-thaw]').forEach(btn => btn.onclick = () => modalForm('解冻间记录 · 计划 ' + btn.dataset.thaw, [
    { name: 'raw_batch_no', label: '原料批号 *', ph: 'RM20260901' },
    { name: 'water_batch_no', label: '用水批次' },
    { name: 'start_at', label: '解冻开始时间', ph: '2026-09-20T06:00' },
    { name: 'end_at', label: '解冻结束时间（>4h 自动报警）', ph: '2026-09-20T09:00' },
    { name: 'center_temp_c', label: '中心温度℃', type: 'number', step: '0.01', def: '2.00' },
    { name: 'operator', label: '操作人' },
  ], async b => {
    const r = await api(`/plans/${btn.dataset.thaw}/thaw`, { method: 'POST', body: b });
    toast(r.auto_event_no ? '解冻超时，已自动生成品控事件 ' + r.auto_event_no : `解冻记录成功（${r.duration_minutes} 分钟）`,
      r.auto_event_no ? 'err' : 'ok');
  }, '提交解冻记录'));

  $$('button[data-proc]').forEach(btn => btn.onclick = () => modalForm('加工线记录 · 计划 ' + btn.dataset.thaw, [
    { name: 'stage', label: '工序', type: 'select', opts: Object.keys(STAGE).map(k => ({ v: k, t: STAGE[k] })) },
    { name: 'operator', label: '操作人' },
    { name: 'value_text', label: '文本值（如调味料批号/包装规格/金检结果）' },
    { name: 'value_num', label: '数值（如单盒重量/库温）', type: 'number', step: '0.01' },
    { name: 'ok', label: '是否合格（金检报警/包装漏气选否）', type: 'select', def: 'true',
      opts: [{ v: 'true', t: '合格' }, { v: 'false', t: '不合格/报警' }] },
    { name: 'note', label: '说明', type: 'textarea', full: true },
  ], async b => {
    b.ok = b.ok === 'true';
    const r = await api(`/plans/${btn.dataset.proc}/process`, { method: 'POST', body: b });
    toast(r.auto_event_no ? '已记录，并自动生成品控事件 ' + r.auto_event_no : '加工记录成功',
      r.auto_event_no ? 'err' : 'ok');
  }, '提交'));

  $$('button[data-finish]').forEach(btn => btn.onclick = () => modalForm('完工：成品批次 + 留样 + 入冷库 · 计划 ' + btn.dataset.finish, [
    { name: 'batch_no', label: '成品批号 *' },
    { name: 'qty_kg', label: '成品重量 kg', type: 'number', step: '0.01', def: '100' },
    { name: 'cold_storage_location', label: '成品冷库库位' },
    { name: 'packaging_spec', label: '渠道定制包装', full: true },
    { name: 'sample_result', label: '留样检测结果', type: 'select', def: 'qualified',
      opts: [{ v: 'qualified', t: '合格' }, { v: 'unqualified', t: '不合格' }, { v: '', t: '待检（留样 pending）' }] },
    { name: 'inspector', label: '留样/检验员' },
  ], async b => {
    if (b.sample_result === '') b.sample_result = null;
    const r = await api(`/plans/${btn.dataset.finish}/finish`, { method: 'POST', body: b });
    toast(`成品 ${r.batch_no} 已入冷库，留样 ${r.sample_no}（${r.sample_result === 'qualified' ? '合格' : r.sample_result === 'unqualified' ? '不合格' : '待检'}）`, 'ok');
  }, '确认完工'));
}

/* ---------- 成品批次 ---------- */

async function pageFinished() {
  const rows = await api('/finished-batches');
  $('#content').innerHTML = pageHead('成品批次 · 留样 · 反向追溯', '从成品反查原料批次、供应商、解冻记录、留样、发货渠道与已售订单') +
    `<div class="panel">${table([
      { t: '成品批号', k: 'batch_no' },
      { t: '产品', k: 'product_name' },
      { t: '渠道定制包装', k: 'packaging_spec' },
      { t: '产量kg', fn: r => fmtNum(r.qty_kg) },
      { t: '可用库存kg', fn: r => fmtNum(r.stock_kg) },
      { t: '冷库库位', k: 'cold_storage_location' },
      { t: '最新留样', k: 'latest_sample', tag: true },
      { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => `<button class="act" data-trace="${esc(r.batch_no)}">反向追溯</button>` +
          (can('quality', 'manager') ? ` <button class="act amber" data-sample="${esc(r.batch_no)}">留样/检测</button>` : '') },
    ], rows)}</div>`;
  $$('button[data-trace]').forEach(b => b.onclick = () => showFinTrace(b.dataset.trace));
  $$('button[data-sample]').forEach(b => b.onclick = () => modalForm('留样检测登记 · ' + b.dataset.sample, [
    { name: 'sample_no', label: '留样号（留空=新登记留样；填写已有号=录入检测结果）' },
    { name: 'result', label: '检测结果', type: 'select',
      opts: [{ v: 'qualified', t: '合格' }, { v: 'unqualified', t: '不合格' }] },
    { name: 'inspector', label: '检验员' },
    { name: 'lab_note', label: '检验备注', type: 'textarea', full: true },
  ], async body => {
    if (!body.sample_no) delete body.sample_no;
    await api(`/finished-batches/${b.dataset.sample}/sample`, { method: 'POST', body });
    toast('留样结果已登记', 'ok');
  }, '提交'));
}

async function showFinTrace(bn) {
  const d = await api(`/finished-batches/${bn}/trace`);
  const f = d.finished;
  modalView(`反向追溯 · 成品批次 ${bn}`, `
    <div class="trace-grid">
      <div class="panel full"><h3>成品档案</h3><div class="panel-body"><dl class="kv" style="grid-template-columns:120px 1fr 120px 1fr">
        <dt>产品</dt><dd>${esc(f.product_name)}</dd><dt>批号</dt><dd>${esc(f.batch_no)}</dd>
        <dt>定制包装</dt><dd>${esc(f.packaging_spec)}</dd><dt>产量</dt><dd>${fmtNum(f.qty_kg)} kg</dd>
        <dt>冷库库位</dt><dd>${esc(f.cold_storage_location)}</dd><dt>状态</dt><dd>${tag(f.status)}</dd>
      </dl></div></div>
      <div class="panel full"><h3>使用的原料批次（供应商 / 入库 / 检疫）</h3><div class="panel-body">${table([
        { t: '原料批号', fn: x => `<button class="act" data-raw="${esc(x.batch_no)}">${esc(x.batch_no)}</button>` },
        { t: '供应商', fn: x => `${esc(x.supplier_code)} ${esc(x.supplier_name)}` },
        { t: '品类', k: 'species' }, { t: '捕捞日期', k: 'catch_date' },
        { t: '入库温度', fn: x => fmtNum(x.inbound_temp_c) }, { t: '检疫证明', k: 'quarantine_cert' },
        { t: '箱号', k: 'box_no' }, { t: '投料kg', fn: x => fmtNum(x.used_qty_kg) }, { t: '状态', k: 'status', tag: true },
      ], d.raw_materials)}</div></div>
      <div class="panel full"><h3>解冻记录</h3><div class="panel-body">${table([
        { t: '原料', k: 'raw_batch_no' }, { t: '开始', k: 'start_at' }, { t: '结束', k: 'end_at' },
        { t: '时长(分)', fn: x => Number(x.duration_minutes) > 240 ? `<b style="color:#b91c1c">${x.duration_minutes} 超时</b>` : x.duration_minutes },
        { t: '中心温度℃', fn: x => fmtNum(x.center_temp_c) }, { t: '用水批次', k: 'water_batch_no' }, { t: '操作人', k: 'operator' },
      ], d.thaw_records)}</div></div>
      <div class="panel"><h3>留样检测</h3><div class="panel-body">${table([
        { t: '留样号', k: 'sample_no' }, { t: '结果', k: 'result', tag: true },
        { t: '检测时间', k: 'tested_at' }, { t: '检验员', k: 'inspector' }, { t: '备注', k: 'lab_note', cls: 'wrap' },
      ], d.samples)}</div></div>
      <div class="panel"><h3>在库/隔离库存</h3><div class="panel-body">${table([
        { t: '库位', k: 'location' }, { t: '数量kg', fn: x => fmtNum(x.qty_kg) }, { t: '状态', k: 'status', tag: true },
      ], d.inventory)}</div></div>
      <div class="panel"><h3>发货渠道</h3><div class="panel-body">${table([
        { t: '发货单', k: 'ship_no' }, { t: '渠道', k: 'channel' }, { t: '类型', k: 'ctype' },
        { t: '箱', k: 'boxes' }, { t: 'kg', fn: x => fmtNum(x.qty_kg) }, { t: '状态', k: 'status', tag: true },
      ], d.shipments)}</div></div>
      <div class="panel"><h3>退回回厂处置</h3><div class="panel-body">${table([
        { t: '退货号', k: 'return_no' }, { t: '箱号', k: 'box_no' }, { t: 'kg', fn: x => fmtNum(x.qty_kg) },
        { t: '处置', fn: x => x.disposition ? tag(DISP[x.disposition] ? x.disposition : x.disposition) : '<span class="tag amber">待判定</span>' },
        { t: '判定人', k: 'decided_by' },
      ], d.returns)}</div></div>
      <div class="panel full"><h3>已售订单（精确到箱）</h3><div class="panel-body">${table([
        { t: '订单号', k: 'order_no' }, { t: '渠道', k: 'channel' }, { t: '箱号', k: 'box_no' },
        { t: '数量', k: 'qty' }, { t: '顾客', k: 'customer' }, { t: '状态', k: 'status', tag: true },
      ], d.orders)}</div></div>
      <div class="panel"><h3>品控事件</h3><div class="panel-body">${table([
        { t: '事件号', k: 'event_no' }, { t: '类型', fn: x => ETYPE[x.etype] },
        { t: '等级', k: 'severity' }, { t: '状态', k: 'status', tag: true }, { t: '描述', k: 'description', cls: 'wrap' },
      ], d.events)}</div></div>
      <div class="panel"><h3>召回记录</h3><div class="panel-body">${table([
        { t: '召回号', k: 'recall_no' }, { t: '级别', k: 'level' },
        { t: '状态', k: 'status', tag: true }, { t: '原因', k: 'reason', cls: 'wrap' },
      ], d.recalls)}</div></div>
    </div>`);
  $$('button[data-raw]').forEach(b => b.onclick = () => { closeModal(); showRawTrace(b.dataset.raw); });
}

/* ---------- 渠道履约 ---------- */

async function pageChannel() {
  const [ships, orders, fbs, channels, feedback] =
    await Promise.all([api('/shipments'), api('/orders'), api('/finished-batches'), api('/channels'), api('/feedback')]);
  state.cache.channels = channels; state.cache.finished = fbs;
  const acts = can('production', 'warehouse', 'manager')
    ? `<button class="act" id="new-ship">📦 发货出库</button>
       <button class="act gray" id="new-order">🧾 登记已售订单</button>
       <button class="act amber" id="new-fb">📣 渠道反馈</button>` : '';
  $('#content').innerHTML = pageHead('渠道 · 发货 · 订单 · 反馈', '渠道定制包装发货；反馈异味/直播间集中退货自动生成品控事件', acts) +
    `<div class="panel"><h3>发货单</h3>${table([
      { t: '发货号', k: 'ship_no' }, { t: '渠道', k: 'channel' }, { t: '类型', k: 'ctype' },
      { t: '箱数', k: 'boxes' }, { t: '重量kg', fn: r => fmtNum(r.qty_kg) },
      { t: '状态', k: 'status', tag: true }, { t: '发货时间', k: 'shipped_at' },
    ], ships)}</div>
    <div class="panel"><h3>渠道反馈（异味 / 集中退货）</h3>${table([
      { t: '渠道', k: 'channel' }, { t: '订单', k: 'order_no' }, { t: '成品', k: 'finished_batch_no' },
      { t: '类型', fn: r => r.ftype === 'odor' ? '反馈异味' : r.ftype === 'live_return' ? '直播集中退货' : esc(r.ftype) },
      { t: '内容', k: 'content', cls: 'wrap' }, { t: '已处理', fn: r => r.handled ? '是' : '否' },
    ], feedback)}</div>
    <div class="panel"><h3>已售订单（最近 500）</h3>${table([
      { t: '订单号', k: 'order_no' }, { t: '发货单', k: 'ship_no' }, { t: '成品', k: 'finished_batch_no' },
      { t: '渠道', k: 'channel' }, { t: '箱号', k: 'box_no' }, { t: '数量', k: 'qty' },
      { t: '顾客', k: 'customer' }, { t: '状态', k: 'status', tag: true },
    ], orders)}</div>`;

  $('#new-ship')?.addEventListener('click', () => modalForm('发货出库（从可用冷库库存扣减）', [
    { name: 'ship_no', label: '发货单号 *' },
    { name: 'channel_code', label: '渠道', type: 'select', opts: channels.map(c => ({ v: c.code, t: c.code + ' ' + c.name })) },
    { name: 'finished_batch_no', label: '成品批号' },
    { name: 'qty_kg', label: '发货重量 kg', type: 'number', step: '0.01', def: '50' },
    { name: 'boxes', label: '箱数', type: 'number', def: '100' },
  ], async b => {
    b.items = [{ finished_batch_no: b.finished_batch_no, boxes: b.boxes, qty_kg: b.qty_kg }];
    await api('/shipments', { method: 'POST', body: b });
    toast('发货成功，库存已扣减', 'ok');
  }, '确认发货'));
  $('#new-order')?.addEventListener('click', () => modalForm('登记已售订单（精确到箱）', [
    { name: 'order_no', label: '订单号 *' }, { name: 'ship_no', label: '发货单号' },
    { name: 'channel_code', label: '渠道', type: 'select', opts: channels.map(c => ({ v: c.code, t: c.name })) },
    { name: 'finished_batch_no', label: '成品批号' },
    { name: 'box_no', label: '箱号 *' }, { name: 'qty', label: '数量', type: 'number', def: '1' },
    { name: 'customer', label: '顾客信息', full: true },
  ], b => api('/orders', { method: 'POST', body: b }).then(() => toast('订单已登记', 'ok')), '保存'));
  $('#new-fb')?.addEventListener('click', () => modalForm('渠道反馈登记', [
    { name: 'channel_code', label: '渠道', type: 'select', opts: channels.map(c => ({ v: c.code, t: c.name })) },
    { name: 'ftype', label: '反馈类型', type: 'select',
      opts: [{ v: 'odor', t: '反馈异味' }, { v: 'live_return', t: '直播间集中退货' }, { v: 'other', t: '其他' }] },
    { name: 'order_no', label: '关联订单号' },
    { name: 'content', label: '反馈内容', type: 'textarea', full: true },
  ], async b => {
    const r = await api('/feedback', { method: 'POST', body: b });
    toast(r.auto_event_no ? '已登记，自动生成品控事件 ' + r.auto_event_no : '反馈已登记', r.auto_event_no ? 'err' : 'ok');
  }, '提交'));
}

/* ---------- 品控事件 ---------- */

async function pageEvents() {
  const rows = await api('/events');
  const acts = can('quality', 'manager') ? `<button class="act red" id="new-ev">🚨 登记品控事件</button>` : '';
  $('#content').innerHTML = pageHead('品控事件中心', '原料温度超标 / 解冻超时 / 调味料用错 / 金检报警 / 包装漏气 / 异味 / 直播退货 / 监管召回', acts) +
    `<div class="panel">${table([
      { t: '事件号', k: 'event_no' },
      { t: '类型', fn: r => ETYPE[r.etype] || r.etype },
      { t: '严重度', fn: r => `<span class="tag ${r.severity === 'critical' ? 'red' : r.severity === 'major' ? 'amber' : 'gray'}">${r.severity}</span>` },
      { t: '原料批次', k: 'raw_batch_no' }, { t: '成品批次', k: 'finished_batch_no' },
      { t: '订单', k: 'order_no' }, { t: '描述', k: 'description', cls: 'wrap' },
      { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => can('quality', 'manager') && r.status === 'open'
        ? `<button class="act red" data-recall="${esc(r.event_no)}">发起召回</button>` : '' },
    ], rows)}</div>`;
  $('#new-ev')?.addEventListener('click', () => modalForm('登记品控事件', [
    { name: 'etype', label: '事件类型', type: 'select', full: true, opts: Object.keys(ETYPE).map(k => ({ v: k, t: ETYPE[k] })) },
    { name: 'severity', label: '严重度', type: 'select', def: 'major',
      opts: [{ v: 'minor', t: '一般' }, { v: 'major', t: '重大' }, { v: 'critical', t: '紧急' }] },
    { name: 'raw_batch_no', label: '原料批号（可空）' },
    { name: 'finished_batch_no', label: '成品批号（可空）' },
    { name: 'order_no', label: '订单号（可空）' },
    { name: 'description', label: '事件描述', type: 'textarea', full: true },
  ], async b => {
    const r = await api('/events', { method: 'POST', body: b });
    toast('事件已登记：' + r.event_no, 'ok');
  }, '登记'));
  $$('button[data-recall]').forEach(b => b.onclick = () => modalForm('发起召回 · 事件 ' + b.dataset.recall, [
    { name: 'level', label: '召回级别', type: 'select', def: 'class2',
      opts: [{ v: 'class1', t: '一级（严重危害）' }, { v: 'class2', t: '二级（一般质量问题）' }, { v: 'class3', t: '三级（标识/包装）' }] },
    { name: 'reason', label: '召回原因', type: 'textarea', full: true, ph: '如：直播间集中反馈异味，怀疑原料温度失控' },
  ], async body => {
    body.event_no = b.dataset.recall;
    const r = await api('/recalls', { method: 'POST', body });
    toast(`召回 ${r.recall_no} 已启动：原料${r.affected_raw}批 / 成品${r.affected_finished}批 / 订单${r.affected_orders}单 / 隔离${r.quarantined_kg}kg`, 'err');
    closeModal(); state.tab = 'recalls'; render();
  }, '确认发起召回'));
}

/* ---------- 召回管理 ---------- */

async function pageRecalls() {
  const rows = await api('/recalls');
  $('#content').innerHTML = pageHead('召回管理', '一键关联采购、仓储、生产、品控、渠道、客服、财务；生成停售/退货/赔偿/供应商追责材料') +
    `<div class="panel">${table([
      { t: '召回号', k: 'recall_no' }, { t: '触发事件', k: 'event_no' },
      { t: '级别', k: 'level' }, { t: '原因', k: 'reason', cls: 'wrap' },
      { t: '材料份数', k: 'docs' }, { t: '状态', k: 'status', tag: true }, { t: '发起时间', k: 'initiated_at' },
      { t: '操作', fn: r => `<button class="act" data-view="${r.id}">召回档案/材料</button>` },
    ], rows)}</div>`;
  $$('button[data-view]').forEach(b => b.onclick = () => showRecall(b.dataset.view));
}

async function showRecall(id) {
  const d = await api(`/recalls/${id}`);
  const rc = d.recall;
  const docCls = { stop_sale: 'stop', return: 'ret', compensation: 'comp', supplier_claim: 'clm' };
  const docName = { stop_sale: '停售通知', return: '退货指令', compensation: '消费者赔偿', supplier_claim: '供应商追责' };
  modalView(`召回档案 ${rc.recall_no}（${rc.level}）`, `
    <div class="flow-note">触发事件 ${esc(rc.event_no)}（${ETYPE[rc.event_type] || rc.event_type}）；状态 ${tag(rc.status)}；原因：${esc(rc.reason)}</div>
    <div class="trace-grid">
      <div class="panel"><h3>① 涉事原料批次（供应商/箱号/库位）</h3><div class="panel-body">${table([
        { t: '原料批号', k: 'batch_no' }, { t: '供应商', k: 'supplier' }, { t: '品类', k: 'species' },
        { t: '入库温度', fn: x => fmtNum(x.inbound_temp_c) }, { t: '箱号', k: 'box_no' },
        { t: '库位', k: 'location' }, { t: '状态', k: 'status', tag: true },
      ], d.raw_batches)}</div></div>
      <div class="panel"><h3>② 涉事成品批次</h3><div class="panel-body">${table([
        { t: '成品批号', k: 'batch_no' }, { t: '产品', k: 'product_name' },
        { t: '定制包装', k: 'packaging_spec' }, { t: '产量kg', fn: x => fmtNum(x.qty_kg) }, { t: '状态', k: 'status', tag: true },
      ], d.finished_batches)}</div></div>
      <div class="panel"><h3>③ 已发货渠道（停售范围）</h3><div class="panel-body">${table([
        { t: '发货单', k: 'ship_no' }, { t: '渠道', k: 'channel' }, { t: '状态', k: 'status', tag: true },
      ], d.shipments)}</div></div>
      <div class="panel"><h3>④ 留样检测结果（判定依据）</h3><div class="panel-body">${table([
        { t: '成品', k: 'finished_batch_no' }, { t: '留样号', k: 'sample_no' },
        { t: '结果', k: 'result', tag: true }, { t: '备注', k: 'lab_note', cls: 'wrap' },
      ], d.samples)}</div></div>
      <div class="panel"><h3>⑤ 在库库存定位（已转隔离）</h3><div class="panel-body">${table([
        { t: '成品', k: 'batch_no' }, { t: '库位', k: 'location' },
        { t: '数量kg', fn: x => fmtNum(x.qty_kg) }, { t: '状态', k: 'status', tag: true },
      ], d.inventory)}</div></div>
      <div class="panel"><h3>⑥ 退货回厂状态</h3><div class="panel-body">${table([
        { t: '退货号', k: 'return_no' }, { t: '箱号', k: 'box_no' }, { t: 'kg', fn: x => fmtNum(x.qty_kg) },
        { t: '处置', fn: x => DISP[x.disposition] ? tag(x.disposition) : '<span class="tag amber">待判定</span>' },
      ], d.returns)}</div></div>
      <div class="panel full"><h3>⑦ 已售订单定位（精确到箱，逐单可赔）</h3><div class="panel-body">${table([
        { t: '订单号', k: 'order_no' }, { t: '渠道', k: 'channel' }, { t: '成品', k: 'finished_batch_no' },
        { t: '箱号', k: 'box_no' }, { t: '数量', k: 'qty' }, { t: '顾客', k: 'customer' },
        { t: '状态', k: 'status', tag: true },
      ], d.orders)}</div></div>
      <div class="panel full"><h3>⑧ 供应商扣款台账</h3><div class="panel-body">${table([
        { t: '扣款单号', k: 'deduction_no' }, { t: '供应商', k: 'supplier' }, { t: '原料', k: 'raw_batch_no' },
        { t: '金额', fn: x => '¥' + fmtNum(x.amount) }, { t: '原因', k: 'reason', cls: 'wrap' }, { t: '状态', k: 'status', tag: true },
      ], d.deductions)}</div></div>
    </div>
    <h3 style="margin:14px 0 8px">⑨ 召回处置材料（停售 / 退货 / 赔偿 / 供应商追责）</h3>
    ${d.actions.length ? d.actions.map(a => `
      <div class="doc-card ${docCls[a.atype]}">
        <div class="doc-title">【${docName[a.atype]}】${esc(a.title)} <span class="tag gray">${esc(a.doc_no)}</span></div>
        <div class="doc-meta">状态：${esc(a.status)}${a.amount ? '　涉及金额/数量：' + fmtNum(a.amount) : ''}</div>
        <div class="doc-content">${esc(a.content)}</div>
      </div>`).join('') : '<div class="flow-note">无材料</div>'}
    ${rc.status === 'executing'
      ? `<div class="form-actions"><button class="act gray" id="close-rc">召回结案</button></div>` : ''}`);
  $('#close-rc')?.addEventListener('click', async () => {
    await api(`/recalls/${id}/close`, { method: 'POST' });
    toast('召回已结案', 'ok'); closeModal(); render();
  });
}

/* ---------- 退货处置 ---------- */

async function pageReturns() {
  const [rows, recalls, fbs] = await Promise.all([api('/returns'), api('/recalls'), api('/finished-batches')]);
  state.cache.recalls = recalls;
  const acts = can('warehouse', 'manager') ? `<button class="act" id="new-rt">📥 退货回厂登记</button>` : '';
  $('#content').innerHTML = pageHead('退货回厂 · 三态处置', '区分 可复检（隔离待判）/ 可返工（转返工库存）/ 必须销毁，库存状态独立流转，避免召回后库存失真', acts) +
    `<div class="panel">${table([
      { t: '退货号', k: 'return_no' }, { t: '召回号', k: 'recall_no' },
      { t: '成品批号', k: 'finished_batch_no' }, { t: '箱号', k: 'box_no' },
      { t: '退回kg', fn: r => fmtNum(r.qty_kg) }, { t: '到厂时间', k: 'arrived_at' },
      { t: '处置结论', fn: r => r.disposition
        ? `<span class="tag ${DISP[r.disposition][0]}">${DISP[r.disposition][1]}</span>`
        : '<span class="tag amber">待判定</span>' },
      { t: '判定人', k: 'decided_by' }, { t: '备注', k: 'note', cls: 'wrap' },
      { t: '操作', fn: r => (!r.disposition && can('quality', 'manager'))
        ? `<button class="act amber" data-decide="${r.id}">判定处置</button>` : '' },
    ], rows)}</div>`;
  $('#new-rt')?.addEventListener('click', () => modalForm('退货回厂登记', [
    { name: 'recall_no', label: '关联召回号', type: 'select', opts: recalls.map(x => ({ v: x.recall_no, t: x.recall_no + ' ' + (x.reason || '') })) },
    { name: 'finished_batch_no', label: '成品批号 *' },
    { name: 'box_no', label: '箱号' },
    { name: 'qty_kg', label: '退回重量 kg', type: 'number', step: '0.01', def: '10' },
    { name: 'note', label: '外观/冷链情况备注', type: 'textarea', full: true },
  ], b => api('/returns', { method: 'POST', body: b }).then(() => toast('退货已登记，等待品控判定', 'ok')), '登记'));
  $$('button[data-decide]').forEach(b => b.onclick = () => modalForm('退货处置判定 · 退货 ' + b.dataset.decide, [
    { name: 'disposition', label: '处置结论', type: 'select', full: true, opts: [
      { v: 'reinspect', t: '可复检 — 隔离待判，检验合格后再定去向' },
      { v: 'rework', t: '可返工 — 转返工区，成品批次标记返工' },
      { v: 'destroy', t: '必须销毁 — 转销毁区，成品批次标记销毁' }] },
    { name: 'note', label: '判定依据', type: 'textarea', full: true },
  ], async body => {
    await api(`/returns/${b.dataset.decide}/dispose`, { method: 'POST', body });
    toast('处置完成，库存状态已同步', 'ok');
  }, '确认判定'));
}

/* ---------- 冷库库存 / 盘点 ---------- */

async function pageInventory() {
  const [inv, sts] = await Promise.all([api('/inventory'), api('/stocktakes')]);
  const acts = can('warehouse', 'manager') ? `<button class="act" id="new-st">📋 冷库盘点</button>` : '';
  $('#content').innerHTML = pageHead('成品冷库 · 库存与盘点', '可用 / 隔离 / 返工 / 销毁 分状态管理，召回隔离不影响账面可用量', acts) +
    `<div class="panel"><h3>实时库存</h3>${table([
      { t: '成品批号', k: 'finished_batch_no' }, { t: '产品', k: 'product_name' },
      { t: '库位', k: 'location' }, { t: '数量kg', fn: r => fmtNum(r.qty_kg) },
      { t: '状态', k: 'status', tag: true }, { t: '更新时间', k: 'updated_at' },
    ], inv)}</div>
    <div class="panel"><h3>盘点记录</h3>${table([
      { t: '盘点单号', k: 'stocktake_no' }, { t: '库位', k: 'location' },
      { t: '盘点时间', k: 'counted_at' }, { t: '操作人', k: 'operator' }, { t: '备注', k: 'note', cls: 'wrap' },
    ], sts)}</div>`;
  $('#new-st')?.addEventListener('click', () => modalForm('冷库盘点（按库位快照系统库存）', [
    { name: 'location', label: '库位（留空=全部冷库）', ph: '冷库C-01' },
    { name: 'operator', label: '盘点人' },
    { name: 'note', label: '备注', type: 'textarea', full: true },
  ], async b => {
    const r = await api('/stocktakes', { method: 'POST', body: b });
    toast(`盘点单 ${r.stocktake_no} 已生成，共 ${r.count} 条`, 'ok');
  }, '生成盘点单'));
}

/* ---------- 财务追偿 ---------- */

async function pageFinance() {
  const [comps, deds] = await Promise.all([api('/compensations'), api('/deductions')]);
  $('#content').innerHTML = pageHead('客服赔付 · 供应商扣款 · 财务追偿', '召回自动生成消费者赔付台账与供应商追责扣款；财务确认支付/扣款') +
    `<div class="panel"><h3>消费者赔付台账（客服 + 财务）</h3>${table([
      { t: '赔付单号', k: 'comp_no' }, { t: '订单', k: 'order_no' }, { t: '渠道', k: 'channel' },
      { t: '成品', k: 'finished_batch_no' }, { t: '金额', fn: r => '¥' + fmtNum(r.amount) },
      { t: '原因', k: 'reason', cls: 'wrap' }, { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => (r.status === 'pending' && can('finance', 'manager'))
        ? `<button class="act green" data-pay="${r.id}">确认支付</button>` : '' },
    ], comps)}</div>
    <div class="panel"><h3>供应商扣款台账</h3>${table([
      { t: '扣款单号', k: 'deduction_no' }, { t: '供应商', fn: r => `${esc(r.supplier_code)} ${esc(r.supplier)}` },
      { t: '原料批次', k: 'raw_batch_no' }, { t: '召回号', k: 'recall_no' },
      { t: '金额', fn: r => '¥' + fmtNum(r.amount) }, { t: '原因', k: 'reason', cls: 'wrap' },
      { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => (r.status !== 'deducted' && can('finance', 'manager'))
        ? `<button class="act amber" data-ded="${r.id}" data-st="confirmed">确认</button>
           <button class="act green" data-ded="${r.id}" data-st="deducted">已扣款</button>` : '' },
    ], deds)}</div>`;
  $$('button[data-pay]').forEach(b => b.onclick = async () => {
    await api(`/compensations/${b.dataset.pay}/pay`, { method: 'POST' });
    toast('赔付已支付，订单状态同步为已赔付', 'ok'); render();
  });
  $$('button[data-ded]').forEach(b => b.onclick = async () => {
    await api(`/deductions/${b.dataset.ded}/status`, { method: 'POST', body: { status: b.dataset.st } });
    toast('扣款状态已更新', 'ok'); render();
  });
}

/* ---------- 监管抽样 ---------- */

async function pageSampling() {
  const rows = await api('/regulatory-samplings');
  const acts = can('quality', 'manager') ? `<button class="act" id="new-gs">🏛️ 监管抽样登记</button>` : '';
  $('#content').innerHTML = pageHead('监管抽样', '监管部门对原料/成品抽样；抽检不合格自动生成监管要求召回事件', acts) +
    `<div class="panel">${table([
      { t: '抽样单号', k: 'sampling_no' }, { t: '监管部门', k: 'regulator' },
      { t: '对象', fn: r => r.target_type === 'raw' ? '原料' : '成品' },
      { t: '原料批次', k: 'raw_batch_no' }, { t: '成品批次', k: 'finished_batch_no' },
      { t: '抽样时间', k: 'sampled_at' }, { t: '结果', k: 'result', tag: true },
      { t: '备注', k: 'note', cls: 'wrap' },
      { t: '操作', fn: r => (r.result === 'pending' && can('quality', 'manager'))
        ? `<button class="act green" data-pass="${r.id}">录入合格</button>
           <button class="act red" data-fail="${r.id}">录入不合格</button>` : '' },
    ], rows)}</div>`;
  $('#new-gs')?.addEventListener('click', () => modalForm('监管抽样登记', [
    { name: 'regulator', label: '监管部门 *', ph: '如：市场监管局食品抽检科' },
    { name: 'target_type', label: '抽样对象', type: 'select',
      opts: [{ v: 'raw', t: '原料批次' }, { v: 'finished', t: '成品批次' }] },
    { name: 'raw_batch_no', label: '原料批号（对象为原料时）' },
    { name: 'finished_batch_no', label: '成品批号（对象为成品时）' },
    { name: 'note', label: '抽样说明', type: 'textarea', full: true },
  ], b => api('/regulatory-samplings', { method: 'POST', body: b }).then(() => toast('抽样已登记', 'ok')), '登记'));
  const result = async (id, result) => {
    const note = result === 'unqualified' ? prompt('请简要记录不合格项（将进入监管召回事件）') || '' : '';
    const r = await api(`/regulatory-samplings/${id}/result`, { method: 'POST', body: { result, note } });
    toast(r.auto_event_no ? '不合格已录入，自动生成监管召回事件 ' + r.auto_event_no : '结果已录入',
      r.auto_event_no ? 'err' : 'ok');
    render();
  };
  $$('button[data-pass]').forEach(b => b.onclick = () => result(b.dataset.pass, 'qualified'));
  $$('button[data-fail]').forEach(b => b.onclick = () => result(b.dataset.fail, 'unqualified'));
}

/* ---------- 直播间集中退货 ---------- */

async function pageLive() {
  const rows = await api('/live-returns');
  const acts = can('quality', 'warehouse', 'manager') ? `<button class="act red" id="new-lr">🎬 登记直播集中退货</button>` : '';
  $('#content').innerHTML = pageHead('直播间集中退货 · 四因分流 · 复盘备货',
    '按 主播承诺不符 / 物流延迟 / 口味问题 / 批次质量异常 分流，分别生成退款、库存退回、客服话术、渠道扣款；复盘对比承诺·到货时效·批次检测并同步渠道，结论影响下场备货', acts) +
    `<div class="panel">${table([
      { t: '退货批次号', k: 'return_no' },
      { t: '直播场次', k: 'live_session' },
      { t: '渠道', k: 'channel' }, { t: '主播', k: 'anchor_name' },
      { t: '成品', k: 'finished_batch_no' },
      { t: '承诺不符', fn: r => r.r_promise ? `<span class="tag red">${r.r_promise}</span>` : '—' },
      { t: '物流延迟', fn: r => r.r_logi ? `<span class="tag amber">${r.r_logi}</span>` : '—' },
      { t: '口味问题', fn: r => r.r_taste ? `<span class="tag purple">${r.r_taste}</span>` : '—' },
      { t: '批次质量', fn: r => r.r_batch ? `<span class="tag red">${r.r_batch}</span>` : '—' },
      { t: '退款合计', fn: r => '¥' + fmtNum(r.refund_total) },
      { t: '扣款合计', fn: r => '¥' + fmtNum(r.charge_total) },
      { t: '退回kg', fn: r => fmtNum(r.returned_kg) },
      { t: '状态', k: 'status', tag: true },
      { t: '操作', fn: r => `<button class="act" data-view="${r.id}">退货档案/复盘</button>` },
    ], rows)}</div>`;
  $('#new-lr')?.addEventListener('click', openLiveReturnForm);
  $$('button[data-view]').forEach(b => b.onclick = () => showLiveReturn(b.dataset.view));
}

function openLiveReturnForm() {
  const reasonOpts = Object.keys(LREASON).map(k => `<option value="${k}">${LREASON[k][1]}</option>`).join('');
  modalView('登记直播集中退货', `
    <form id="lr-form" class="form-grid">
      <label>直播场次号 *<input name="live_session" placeholder="如：抖音海鲜专场 LS20260920" required></label>
      <label>渠道<select name="channel_code">
        <option value="CH03">CH03 抖音海鲜直播间</option>
        <option value="CH02">CH02 天猫旗舰店</option></select></label>
      <label>主播<input name="anchor_name" placeholder="主播昵称"></label>
      <label>主涉事成品批号<input name="finished_batch_no" placeholder="FG20260912-02"></label>
      <label>承诺送达时效(小时)<input name="promised_arrival_hours" type="number" step="0.1" value="48"></label>
      <label>单价(元/盒)<input name="unit_price" type="number" step="0.01" value="59.9"></label>
      <label class="full">主播承诺品质/规格快照（口播录屏摘录）<input name="promised_quality"
        placeholder="如：现剥大颗虾仁，冷链直达，坏单包赔"></label>
      <label class="full">承诺保鲜/保质期<input name="promised_shelf_life" placeholder="如：冷藏保鲜 7 天"></label>
      <label class="full">备注<textarea name="note" rows="2"></textarea></label>
      <div class="full">
        <div class="lr-row-head">退货明细（逐单归入四类原因，至少 1 条）
          <button type="button" class="act gray" id="lr-add">➕ 添加退货单</button></div>
        <table class="lr-items"><thead><tr>
          <th>订单号</th><th>箱号</th><th>盒数</th><th>退回kg</th><th>原因</th>
          <th>实际到货h</th><th>到厂质量</th><th>凭证/说明</th><th></th>
        </tr></thead><tbody id="lr-rows"></tbody></table>
      </div>
      <div class="full form-actions">
        <button type="button" class="act gray" id="mcancel">取消</button>
        <button type="submit" class="act red">提交集中退货（自动生成四类单据）</button>
      </div>
    </form>`);
  $('#mcancel').onclick = closeModal;
  const body = $('#lr-rows');
  const addRow = (def = {}) => {
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td><input name="order_no" placeholder="OD..." value="${esc(def.order_no || '')}"></td>
      <td><input name="box_no" value="${esc(def.box_no || '')}"></td>
      <td><input name="qty" type="number" step="0.01" value="${def.qty ?? 1}"></td>
      <td><input name="qty_kg" type="number" step="0.01" value="${def.qty_kg ?? 0.25}"></td>
      <td><select name="reason">${reasonOpts}</select></td>
      <td><input name="actual_arrival_hours" type="number" step="0.1" value="${def.actual ?? ''}" placeholder="—"></td>
      <td><select name="quality_result">
        <option value="">未判</option><option value="qualified">合格</option><option value="unqualified">不合格</option></select></td>
      <td><input name="evidence" placeholder="凭证/差评说明"></td>
      <td><button type="button" class="act gray" data-del>删</button></td>`;
    if (def.reason) tr.querySelector('[name=reason]').value = def.reason;
    if (def.quality) tr.querySelector('[name=quality_result]').value = def.quality;
    tr.querySelector('[data-del]').onclick = () => tr.remove();
    body.appendChild(tr);
  };
  addRow(); addRow();
  $('#lr-add').onclick = () => addRow();
  $('#lr-form').addEventListener('submit', async e => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const head = {};
    ['live_session','channel_code','anchor_name','finished_batch_no','promised_quality','promised_shelf_life','note']
      .forEach(k => head[k] = fd.get(k));
    head.promised_arrival_hours = Number(fd.get('promised_arrival_hours'));
    head.unit_price = Number(fd.get('unit_price'));
    head.items = [];
    $$('#lr-rows tr').forEach(tr => {
      const g = n => tr.querySelector(`[name=${n}]`);
      const order_no = g('order_no').value.trim();
      if (!order_no) return;
      head.items.push({
        order_no, box_no: g('box_no').value.trim(),
        qty: Number(g('qty').value) || 0, qty_kg: Number(g('qty_kg').value) || 0,
        reason: g('reason').value, actual_arrival_hours: g('actual_arrival_hours').value === '' ? null : Number(g('actual_arrival_hours').value),
        quality_result: g('quality_result').value, evidence: g('evidence').value.trim(),
      });
    });
    if (!head.items.length) { toast('至少填写一条带订单号的退货明细', 'err'); return; }
    try {
      const r = await api('/live-returns', { method: 'POST', body: head });
      toast(`集中退货 ${r.return_no} 已登记：退款 ¥${r.refund_total} / 扣款 ¥${r.charge_total} / 退回 ${r.stock_return_kg}kg` +
        (r.auto_event_no ? '，批次质量异常已自动生成品控事件 ' + r.auto_event_no : ''), r.auto_event_no ? 'err' : 'ok');
      closeModal(); render();
    } catch (err) { toast(err.message, 'err'); }
  });
}

async function showLiveReturn(id) {
  await renderLiveDetail(id);
}

async function renderLiveDetail(id) {
  const d = await api(`/live-returns/${id}`);
  const b = d.batch;
  const reviewed = (d.review || []).length > 0;
  const rv = reviewed ? d.review[0] : null;

  const actionBtns = [];
  if (b.status === 'registered' && can('quality', 'manager'))
    actionBtns.push(`<button class="act red" id="lr-review">🔍 生成复盘（对比承诺·到货时效·批次检测）</button>`);
  if (b.status === 'reviewed' && can('quality', 'manager'))
    actionBtns.push(`<button class="act" id="lr-sync">📡 同步渠道复盘</button>`);
  if ((b.status === 'synced' || b.status === 'reviewed') && can('quality', 'manager'))
    actionBtns.push(`<button class="act gray" id="lr-close">结案</button>`);

  modalView(`直播集中退货 ${b.return_no}（${b.live_session}）`, `
    <div class="flow-note">
      渠道：${esc(b.channel)}　主播：${esc(b.anchor_name)}　成品：${esc(b.finished_batch_no)}　状态：${tag(b.status)}<br>
      <b>主播承诺快照：</b>${esc(b.promised_quality)}；${esc(b.promised_shelf_life)}；承诺 ${b.promised_arrival_hours ?? '—'} 小时送达。
    </div>
    ${actionBtns.join(' ')}
    <div class="trace-grid">
      <div class="panel full"><h3>退货明细（四类原因）</h3><div class="panel-body">${table([
        { t: '订单号', k: 'order_no' }, { t: '箱号', k: 'box_no' }, { t: '盒数', k: 'qty' },
        { t: '退回kg', fn: x => fmtNum(x.qty_kg) },
        { t: '原因', fn: x => `<span class="tag ${LREASON[x.reason][0]}">${LREASON[x.reason][1]}</span>` },
        { t: '承诺/实际h', fn: x => `${x.promised_arrival_hours ?? '—'} / ${x.actual_arrival_hours ?? '—'}` },
        { t: '到厂质量', fn: x => x.quality_result ? tag(x.quality_result) : '待判' },
        { t: '凭证', k: 'evidence', cls: 'wrap' },
      ], d.items)}</div></div>

      <div class="panel"><h3>① 退款单（分别生成）</h3><div class="panel-body">${table([
        { t: '退款单号', k: 'refund_no' }, { t: '订单', k: 'order_no' },
        { t: '金额', fn: x => '¥' + fmtNum(x.amount) }, { t: '原因', k: 'reason', cls: 'wrap' },
        { t: '状态', k: 'status', tag: true },
        { t: '操作', fn: x => (x.status === 'pending' && can('finance', 'manager'))
          ? `<button class="act green" data-payrefund="${x.id}">退款</button>` : '' },
      ], d.refunds)}</div></div>

      <div class="panel"><h3>② 库存退回（建议处置 → 三态判定）</h3><div class="panel-body">${table([
        { t: '库存单号', k: 'stock_no' }, { t: '退货登记', k: 'return_goods_no' },
        { t: '成品', k: 'finished_batch_no' }, { t: '箱号', k: 'box_no' },
        { t: 'kg', fn: x => fmtNum(x.qty_kg) },
        { t: '建议处置', fn: x => DISP[x.suggested_disposition] ? tag(x.suggested_disposition) : x.suggested_disposition },
        { t: '去向库位', k: 'target_location' },
      ], d.stocks)}</div></div>

      <div class="panel full"><h3>③ 客服话术（按原因分别生成）</h3><div class="panel-body">
        ${d.scripts.length ? d.scripts.map(sc => `
          <div class="doc-card ${sc.reason === 'batch_quality' ? 'clm' : sc.reason === 'logistics_delay' ? 'ret' : 'comp'}">
            <div class="doc-title">【${LREASON[sc.reason] ? LREASON[sc.reason][1] : sc.reason}】${esc(sc.title)} <span class="tag gray">${esc(sc.script_no)}</span></div>
            <div class="doc-content">${esc(sc.content)}</div>
          </div>`).join('') : '<div class="flow-note">无话术</div>'}
      </div></div>

      <div class="panel full"><h3>④ 渠道扣款（按原因适用不同扣款）</h3><div class="panel-body">${table([
        { t: '扣款单号', k: 'charge_no' }, { t: '渠道', k: 'channel' },
        { t: '扣款类型', fn: x => CHARGE_TYPE[x.charge_type] || x.charge_type },
        { t: '金额', fn: x => '¥' + fmtNum(x.amount) }, { t: '原因', k: 'reason', cls: 'wrap' },
        { t: '状态', k: 'status', tag: true },
        { t: '操作', fn: x => (x.status !== 'deducted' && can('finance', 'manager'))
          ? `<button class="act amber" data-charge="${x.id}" data-st="confirmed">确认</button>
             <button class="act green" data-charge="${x.id}" data-st="deducted">已扣款</button>` : '' },
      ], d.charges)}</div></div>
    </div>

    <h3 style="margin:14px 0 8px">🔍 复盘：主播承诺 / 实际到货时效 / 批次检测对比</h3>
    ${reviewed ? `
      <div class="trace-grid">
        <div class="panel"><h3>主播承诺对比</h3><div class="panel-body"><div class="doc-content">${esc(rv.promise_compare)}</div></div></div>
        <div class="panel"><h3>实际到货时效对比</h3><div class="panel-body"><div class="doc-content">${esc(rv.logistics_compare)}</div></div></div>
        <div class="panel full"><h3>批次留样检测对比</h3><div class="panel-body"><div class="doc-content">${esc(rv.quality_compare)}</div></div></div>
      </div>
      <h3 style="margin:14px 0 8px">复盘建议（渠道扣款 / 质量整改 / 物流追责）</h3>
      ${d.suggestions.map(sg => `
        <div class="doc-card ${sg.stype === 'channel_deduction' ? 'clm' : sg.stype === 'logistics_accountability' ? 'ret' : 'stop'}">
          <div class="doc-title">【${SUG_TYPE[sg.stype] ? `<span class="tag ${SUG_TYPE[sg.stype][0]}">${SUG_TYPE[sg.stype][1]}</span>` : sg.stype}】${esc(sg.title)}
            ${sg.amount ? `<span class="tag gray">¥${fmtNum(sg.amount)}</span>` : ''}</div>
          <div class="doc-content">${esc(sg.content)}</div>
        </div>`).join('')}
      <h3 style="margin:14px 0 8px">📦 下一场直播备货建议（复盘结果影响排播备货）</h3>
      <div class="panel"><div class="table-wrap"><table><thead><tr>
        <th>产品</th><th>本场发货kg</th><th>退回kg</th><th>退货率</th><th>下场建议备货kg</th><th>依据</th>
      </tr></thead><tbody>${d.restocks.map(x => `<tr>
        <td>${esc(x.product_name)}</td><td>${fmtNum(x.shipped_kg)}</td><td>${fmtNum(x.returned_kg)}</td>
        <td>${fmtNum(x.return_rate)}%</td>
        <td><b style="color:${Number(x.suggest_qty_kg) === 0 ? '#b91c1c' : '#15803d'}">${fmtNum(x.suggest_qty_kg)}</b></td>
        <td class="wrap">${esc(x.reason)}</td></tr>`).join('')}</tbody></table></div></div>
      <h3 style="margin:14px 0 8px">📡 渠道复盘同步</h3>
      ${d.syncs.length ? d.syncs.map(sy => `
        <div class="doc-card ret">
          <div class="doc-title">${esc(sy.sync_no)}　${tag(sy.status)}${sy.synced_at ? '　' + esc(sy.synced_at) : ''}</div>
          <div class="doc-content">${esc(sy.content)}</div>
          ${sy.response ? `<div class="doc-meta">渠道回执：${esc(sy.response)}</div>`
            : (sy.status === 'synced' && can('quality', 'manager') ? `<div class="form-actions"><button class="act" data-syncack="${sy.id}">登记渠道回执确认</button></div>` : '')}
        </div>`).join('') : '<div class="flow-note">尚未同步渠道</div>'}
    ` : '<div class="flow-note">该场退货尚未复盘。点击上方「生成复盘」，系统将对比主播承诺、实际到货时效与批次留样检测，输出渠道扣款 / 质量整改 / 物流追责建议，并计算下场备货调整。</div>'}`);

  $('#lr-review')?.addEventListener('click', async () => {
    try {
      const r = await api(`/live-returns/${id}/review`, { method: 'POST' });
      toast(`复盘完成：生成 ${r.suggestions_count} 条建议，下场备货建议已更新`, 'ok');
      renderLiveDetail(id);
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#lr-sync')?.addEventListener('click', async () => {
    try {
      const r = await api(`/live-returns/${id}/sync`, { method: 'POST' });
      toast(`渠道复盘已同步（${r.sync_no}），含扣款 ¥${r.channel_deduction_total} 与 ${r.restock_products} 个品的备货调整`, 'ok');
      renderLiveDetail(id);
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#lr-close')?.addEventListener('click', async () => {
    await api(`/live-returns/${id}/close`, { method: 'POST' });
    toast('直播退货已结案', 'ok'); closeModal(); render();
  });
  $$('#modal button[data-payrefund]').forEach(btn => btn.onclick = async () => {
    await api(`/live-refunds/${btn.dataset.payrefund}/pay`, { method: 'POST' });
    toast('退款已支付', 'ok'); renderLiveDetail(id);
  });
  $$('#modal button[data-charge]').forEach(btn => btn.onclick = async () => {
    await api(`/live-charges/${btn.dataset.charge}/status`, { method: 'POST', body: { status: btn.dataset.st } });
    toast('渠道扣款状态已更新', 'ok'); renderLiveDetail(id);
  });
  $$('#modal button[data-syncack]').forEach(btn => btn.onclick = async () => {
    const response = prompt('登记渠道回执（渠道对扣款/整改/备货的确认意见）') || '';
    if (!response) return;
    await api(`/live-returns/${id}/syncs/${btn.dataset.syncack}/respond`, { method: 'POST', body: { response } });
    toast('渠道回执已登记', 'ok'); renderLiveDetail(id);
  });
}

boot();
