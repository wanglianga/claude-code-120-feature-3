package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ================= 直播间集中退货 =================
//
// 业务流：直播场次（主播承诺）→ 开启集中退货 → 逐笔登记退货（区分四类原因，
// 分别生成 退款/库存退回/客服话术/渠道扣款 四项处置）→ 退货结束生成复盘
// （对比主播承诺、实际到货时效、批次检测 → 渠道扣款/质量整改/物流追责建议）
// → 复盘同步渠道（批次质量异常自动升级品控事件，生成下一场直播备货建议）。

// 四类退货原因：名称 / 渠道扣款比例（按退款额） / 客服话术模板
var liveReasons = map[string]struct {
	Name         string
	DeductRate   float64
	ScriptPrompt string
}{
	"promise_mismatch": {"主播承诺不符", 1.0,
		"您好，非常抱歉本次直播商品与主播承诺不符（%s）。我们已为您办理全额退款 %.2f 元，无需寄回商品；后续直播承诺文案将经品控审核后再上架，感谢您的监督。"},
	"logistics_delay": {"物流延迟", 0,
		"您好，您的订单因冷链物流延迟（实际到货约 %s 小时，超出承诺 %s 小时）影响了体验，我们已为您退款 %.2f 元，商品无需寄回。我们已同步启动对承运商的追责流程，抱歉久等。"},
	"taste_problem": {"口味问题", 0.3,
		"您好，很抱歉商品口味未达预期（%s）。我们已为您办理退款 %.2f 元，并将您的反馈同步给调味工艺组整改；欢迎下次直播再给我们一次机会。"},
	"batch_quality": {"批次质量异常", 0,
		"您好，经核实您反馈的商品存在批次质量异常。我们已第一时间为您全额退款 %.2f 元，并对该批次启动复检与召回评估，请您不要继续食用。给您带来担忧，我们深表歉意。"},
}

func liveReasonName(cat string) string {
	if r, ok := liveReasons[cat]; ok {
		return r.Name
	}
	return cat
}

// ---------- 直播场次 ----------

func (s *Server) listLiveSessions(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.pool.Query(r.Context(), `
		SELECT ls.id,ls.session_no,ls.title,ls.anchor,ch.code AS channel_code,ch.name AS channel,
		       ls.live_at::text,ls.promised_delivery_hours,ls.promise_text,ls.status,
		       (SELECT count(*) FROM live_return_batches b WHERE b.session_id=ls.id) AS return_batches,
		       (SELECT COALESCE(sum(e.amount),0) FROM live_return_orders e
		          JOIN live_return_batches b ON b.id=e.batch_id WHERE b.session_id=ls.id) AS refund_total
		FROM live_sessions ls LEFT JOIN channels ch ON ch.id=ls.channel_id
		ORDER BY ls.live_at DESC`)
	defer rows.Close()
	writeJSON(w, 200, scanMaps(rows))
}

type liveSessionReq struct {
	SessionNo             string `json:"session_no"`
	ChannelCode           string `json:"channel_code"`
	Anchor                string `json:"anchor"`
	Title                 string `json:"title"`
	LiveAt                string `json:"live_at"` // 2026-09-20T19:00
	PromisedDeliveryHours int    `json:"promised_delivery_hours"`
	PromiseText           string `json:"promise_text"`
}

func (s *Server) createLiveSession(w http.ResponseWriter, r *http.Request) {
	var req liveSessionReq
	if err := decode(r, &req); err != nil || req.Anchor == "" || req.Title == "" {
		writeErr(w, 400, "缺少 anchor/title")
		return
	}
	if req.SessionNo == "" {
		req.SessionNo = docNo("LS")
	}
	if req.PromisedDeliveryHours <= 0 {
		req.PromisedDeliveryHours = 48
	}
	liveAt := req.LiveAt
	if liveAt == "" {
		liveAt = "now()"
	}
	u := currentUser(r)
	var id int64
	err := s.pool.QueryRow(r.Context(), `
		INSERT INTO live_sessions(session_no,channel_id,anchor,title,live_at,promised_delivery_hours,promise_text,created_by)
		SELECT $1,(SELECT id FROM channels WHERE code=$2),$3,$4,
		       CASE WHEN $5='now()' THEN now() ELSE $5::timestamptz END,
		       $6,$7,$8
		RETURNING id`,
		req.SessionNo, req.ChannelCode, req.Anchor, req.Title, liveAt,
		req.PromisedDeliveryHours, req.PromiseText, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 400, "直播场次创建失败："+err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "session_no": req.SessionNo})
}

func (s *Server) getLiveSession(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ctx := r.Context()
	var no, title, anchor, liveAt, promise, status string
	var channel *string
	var promised int
	err := s.pool.QueryRow(ctx, `
		SELECT ls.session_no,ls.title,ls.anchor,ls.live_at::text,ls.promised_delivery_hours,
		       COALESCE(ls.promise_text,''),ls.status,ch.name
		FROM live_sessions ls LEFT JOIN channels ch ON ch.id=ls.channel_id WHERE ls.id=$1`, id).
		Scan(&no, &title, &anchor, &liveAt, &promised, &promise, &status, &channel)
	if err != nil {
		writeErr(w, 404, "直播场次不存在")
		return
	}
	qm := func(sql string, args ...any) []map[string]any {
		rows, qerr := s.pool.Query(ctx, sql, args...)
		if qerr != nil {
			return []map[string]any{}
		}
		defer rows.Close()
		return scanMaps(rows)
	}
	batches := qm(`
		SELECT b.id,b.batch_no,f.batch_no AS finished_batch_no,f.product_name,b.status,
		       b.opened_at::text,b.closed_at::text,b.note,
		       (SELECT count(*) FROM live_return_orders e WHERE e.batch_id=b.id) AS entries,
		       (SELECT COALESCE(sum(e.amount),0) FROM live_return_orders e WHERE e.batch_id=b.id) AS refund_total
		FROM live_return_batches b LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.session_id=$1 ORDER BY b.opened_at DESC`, id)
	reviews := qm(`
		SELECT rv.id,rv.review_no,rv.suggestion,rv.refund_total,rv.deduction_amount,
		       rv.restock_adjustment,rv.status,rv.reviewed_at::text,rv.synced_at::text
		FROM channel_reviews rv
		JOIN live_return_batches b ON b.id=rv.batch_id
		WHERE b.session_id=$1 ORDER BY rv.reviewed_at DESC`, id)
	writeJSON(w, 200, map[string]any{
		"session": map[string]any{
			"id": id, "session_no": no, "title": title, "anchor": anchor,
			"channel": channel, "live_at": liveAt, "promised_delivery_hours": promised,
			"promise_text": promise, "status": status,
		},
		"return_batches": batches,
		"reviews":        reviews,
	})
}

// ---------- 集中退货批次 ----------

func (s *Server) listLiveReturnBatches(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.pool.Query(r.Context(), `
		SELECT b.id,b.batch_no,ls.session_no,ls.title AS session_title,ls.anchor,
		       f.batch_no AS finished_batch_no,f.product_name,
		       b.status,b.opened_at::text,b.closed_at::text,b.note,
		       (SELECT count(*) FROM live_return_orders e WHERE e.batch_id=b.id) AS entries,
		       (SELECT COALESCE(sum(e.amount),0) FROM live_return_orders e WHERE e.batch_id=b.id) AS refund_total,
		       (SELECT count(*) FROM live_return_disposals d
		          JOIN live_return_orders e ON e.id=d.entry_id
		          WHERE e.batch_id=b.id AND d.status='pending') AS pending_disposals,
		       (SELECT rv.review_no FROM channel_reviews rv WHERE rv.batch_id=b.id) AS review_no
		FROM live_return_batches b
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		ORDER BY b.opened_at DESC`)
	defer rows.Close()
	writeJSON(w, 200, scanMaps(rows))
}

type liveReturnOpenReq struct {
	SessionNo       string `json:"session_no"`
	FinishedBatchNo string `json:"finished_batch_no"`
	Note            string `json:"note"`
}

// openLiveReturn 开启一场直播针对某成品批次的集中退货。
func (s *Server) openLiveReturn(w http.ResponseWriter, r *http.Request) {
	var req liveReturnOpenReq
	if err := decode(r, &req); err != nil || req.SessionNo == "" || req.FinishedBatchNo == "" {
		writeErr(w, 400, "缺少 session_no/finished_batch_no")
		return
	}
	u := currentUser(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var sessionID int64
	var st string
	if err := tx.QueryRow(ctx,
		`SELECT id,status FROM live_sessions WHERE session_no=$1 FOR UPDATE`, req.SessionNo).
		Scan(&sessionID, &st); err != nil {
		writeErr(w, 404, "直播场次不存在："+req.SessionNo)
		return
	}
	if st == "reviewed" {
		writeErr(w, 409, "该场次退货已复盘结案，不能再开启集中退货")
		return
	}
	batchNo := docNo("LRB")
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO live_return_batches(batch_no,session_id,finished_batch_id,opened_by,note)
		SELECT $1,$2,(SELECT id FROM finished_batches WHERE batch_no=$3),$4,$5
		RETURNING id`, batchNo, sessionID, req.FinishedBatchNo, u.ID, req.Note).Scan(&id); err != nil {
		writeErr(w, 400, "集中退货开启失败："+err.Error())
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE live_sessions SET status='returning' WHERE id=$1 AND status='live'`, sessionID)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "batch_no": batchNo})
}

// getLiveReturnBatch 退货批次明细：退货登记 + 每笔四项处置 + 复盘。
func (s *Server) getLiveReturnBatch(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ctx := r.Context()

	var batchNo, status, openedAt, note string
	var closedAt *string
	var sessionNo, title, anchor, promise string
	var fBatchNo, product string
	var promised int
	err := s.pool.QueryRow(ctx, `
		SELECT b.batch_no,b.status,b.opened_at::text,b.closed_at::text,COALESCE(b.note,''),
		       ls.session_no,ls.title,ls.anchor,COALESCE(ls.promise_text,''),ls.promised_delivery_hours,
		       COALESCE(f.batch_no,''),COALESCE(f.product_name,'')
		FROM live_return_batches b
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.id=$1`, id).
		Scan(&batchNo, &status, &openedAt, &closedAt, &note,
			&sessionNo, &title, &anchor, &promise, &promised, &fBatchNo, &product)
	if err != nil {
		writeErr(w, 404, "集中退货批次不存在")
		return
	}

	erows, _ := s.pool.Query(ctx, `
		SELECT e.id,e.entry_no,o.order_no,e.reason_cat,e.reason_detail,e.qty,e.qty_kg,e.amount,
		       e.promised_delivery_hours,e.actual_delivery_hours,COALESCE(e.sample_result,''),
		       e.created_at::text
		FROM live_return_orders e LEFT JOIN orders o ON o.id=e.order_id
		WHERE e.batch_id=$1 ORDER BY e.created_at`, id)
	entries := []map[string]any{}
	var entryIDs []int64
	for erows.Next() {
		var eid, promisedH int64
		var eno, cat string
		var orderNo, detail, sample, createdAt *string
		var qty, qtyKg, amount float64
		var actualH *float64
		_ = erows.Scan(&eid, &eno, &orderNo, &cat, &detail, &qty, &qtyKg, &amount,
			&promisedH, &actualH, &sample, &createdAt)
		entryIDs = append(entryIDs, eid)
		entries = append(entries, map[string]any{
			"id": eid, "entry_no": eno, "order_no": orderNo, "reason_cat": cat,
			"reason_name": liveReasonName(cat), "reason_detail": detail,
			"qty": qty, "qty_kg": qtyKg, "amount": amount,
			"promised_delivery_hours": promisedH, "actual_delivery_hours": actualH,
			"sample_result": sample, "created_at": createdAt,
		})
	}
	erows.Close()

	dmap := map[int64][]map[string]any{}
	if len(entryIDs) > 0 {
		drows, _ := s.pool.Query(ctx, `
			SELECT id,entry_id,doc_no,dtype,title,content,amount,status,
			       COALESCE(executed_by,''),executed_at::text
			FROM live_return_disposals WHERE entry_id=ANY($1::bigint[])
			ORDER BY CASE dtype WHEN 'refund' THEN 1 WHEN 'restock' THEN 2
			                    WHEN 'script' THEN 3 WHEN 'channel_deduction' THEN 4 END`, entryIDs)
		for drows.Next() {
			var did, entryID int64
			var docNoV, dtype, titleV, content, stV, execBy string
			var amount float64
			var execAt *string
			_ = drows.Scan(&did, &entryID, &docNoV, &dtype, &titleV, &content, &amount, &stV, &execBy, &execAt)
			dmap[entryID] = append(dmap[entryID], map[string]any{
				"id": did, "doc_no": docNoV, "dtype": dtype, "title": titleV,
				"content": content, "amount": amount, "status": stV,
				"executed_by": execBy, "executed_at": execAt,
			})
		}
		drows.Close()
	}
	for _, e := range entries {
		e["disposals"] = dmap[e["id"].(int64)]
	}

	qm := func(sql string, args ...any) []map[string]any {
		rows, qerr := s.pool.Query(ctx, sql, args...)
		if qerr != nil {
			return []map[string]any{}
		}
		defer rows.Close()
		return scanMaps(rows)
	}
	reviews := qm(`
		SELECT rv.id,rv.review_no,ch.name AS channel,rv.promise_summary,rv.delivery_summary,
		       rv.sample_summary,rv.suggestion,rv.suggestion_detail,rv.refund_total,
		       rv.deduction_amount,rv.restock_adjustment,rv.status,rv.reviewed_at::text,rv.synced_at::text
		FROM channel_reviews rv LEFT JOIN channels ch ON ch.id=rv.channel_id
		WHERE rv.batch_id=$1`, id)

	writeJSON(w, 200, map[string]any{
		"batch": map[string]any{
			"id": id, "batch_no": batchNo, "status": status, "opened_at": openedAt,
			"closed_at": closedAt, "note": note,
			"session_no": sessionNo, "session_title": title, "anchor": anchor,
			"promise_text": promise, "promised_delivery_hours": promised,
			"finished_batch_no": fBatchNo, "product_name": product,
		},
		"entries": entries,
		"reviews": reviews,
	})
}

// ---------- 退货登记（四类原因，分别生成四项处置） ----------

type liveEntryReq struct {
	OrderNo               string   `json:"order_no"`
	ReasonCat             string   `json:"reason_cat"`
	ReasonDetail          string   `json:"reason_detail"`
	Qty                   float64  `json:"qty"`
	QtyKg                 *float64 `json:"qty_kg"`
	UnitPrice             *float64 `json:"unit_price"`
	ActualDeliveryHours   *float64 `json:"actual_delivery_hours"`
	PromisedDeliveryHours *int     `json:"promised_delivery_hours"`
	SampleResult          string   `json:"sample_result"`
}

func (s *Server) addLiveReturnEntry(w http.ResponseWriter, r *http.Request) {
	batchID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req liveEntryReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "字段错误")
		return
	}
	rule, ok := liveReasons[req.ReasonCat]
	if !ok {
		writeErr(w, 400, "reason_cat 必须为 promise_mismatch/logistics_delay/taste_problem/batch_quality")
		return
	}
	if req.Qty <= 0 {
		req.Qty = 1
	}
	u := currentUser(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	// 退货批次与场次快照
	var st, fBatchNo, product string
	var finID, sessionID int64
	var promisedH int
	if err := tx.QueryRow(ctx, `
		SELECT b.status,b.finished_batch_id,b.session_id,
		       COALESCE(f.batch_no,''),COALESCE(f.product_name,''),ls.promised_delivery_hours
		FROM live_return_batches b
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.id=$1 FOR UPDATE OF b`, batchID).
		Scan(&st, &finID, &sessionID, &fBatchNo, &product, &promisedH); err != nil {
		writeErr(w, 404, "集中退货批次不存在")
		return
	}
	if st != "open" {
		writeErr(w, 409, "该集中退货已结束（已复盘），不能再登记退货")
		return
	}

	// 默认值：承诺时效取场次快照；单价默认 59.9；退回重量默认 0.25kg/盒
	if req.PromisedDeliveryHours != nil {
		promisedH = *req.PromisedDeliveryHours
	}
	unitPrice := 59.9
	if req.UnitPrice != nil && *req.UnitPrice > 0 {
		unitPrice = *req.UnitPrice
	}
	qtyKg := math.Round(req.Qty*0.25*100) / 100
	if req.QtyKg != nil && *req.QtyKg > 0 {
		qtyKg = *req.QtyKg
	}
	amount := math.Round(req.Qty*unitPrice*100) / 100
	detail := req.ReasonDetail
	if detail == "" {
		detail = rule.Name
	}

	// 关联订单（可空），已售订单标记退回
	var orderID *int64
	if req.OrderNo != "" {
		var oid int64
		if err := tx.QueryRow(ctx, `SELECT id FROM orders WHERE order_no=$1`, req.OrderNo).Scan(&oid); err != nil {
			writeErr(w, 404, "订单不存在："+req.OrderNo)
			return
		}
		orderID = &oid
		_, _ = tx.Exec(ctx, `UPDATE orders SET status='returned' WHERE id=$1 AND status='sold'`, oid)
	}

	entryNo := docNo("LE")
	var entryID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO live_return_orders
		  (entry_no,batch_id,order_id,reason_cat,reason_detail,qty,qty_kg,amount,
		   promised_delivery_hours,actual_delivery_hours,sample_result,registered_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		entryNo, batchID, orderID, req.ReasonCat, detail, req.Qty, qtyKg, amount,
		promisedH, req.ActualDeliveryHours, req.SampleResult, u.ID).Scan(&entryID); err != nil {
		writeErr(w, 400, "退货登记失败："+err.Error())
		return
	}

	// 分别生成四项处置：退款 / 库存退回 / 客服话术 / 渠道扣款
	orderRef := "未关联订单"
	if req.OrderNo != "" {
		orderRef = "订单 " + req.OrderNo
	}
	actualRef := "未登记"
	if req.ActualDeliveryHours != nil {
		actualRef = fmt.Sprintf("%.1f", *req.ActualDeliveryHours)
	}
	deduct := math.Round(amount*rule.DeductRate*100) / 100
	deductNote := map[float64]string{
		1.0: "主播承诺不符，按退款额 100% 向渠道（主播/机构）扣款",
		0.3: "口味问题渠道选品连带，按退款额 30% 扣款",
		0.0: "非渠道责任（物流/工厂质量），渠道扣款 0 元",
	}[rule.DeductRate]

	disposals := []struct {
		dtype, title, content string
		amount                float64
	}{
		{"refund", "退款-" + entryNo,
			fmt.Sprintf("向消费者退款 %.2f 元（%.0f 盒 × %.2f 元/盒），%s，原因：%s。",
				amount, req.Qty, unitPrice, orderRef, rule.Name), amount},
		{"restock", "库存退回-" + entryNo,
			fmt.Sprintf("退回商品 %.0f 盒 / %.2f kg（成品 %s %s）登记回厂，入「直播退货仓」隔离待判，后续按可复检/可返工/必须销毁三态处置。",
				req.Qty, qtyKg, fBatchNo, product), qtyKg},
		{"script", "客服话术-" + entryNo,
			fmt.Sprintf(rule.ScriptPrompt, detail, amount), 0},
		{"channel_deduction", "渠道扣款-" + entryNo,
			fmt.Sprintf("%s：%.2f 元（退款基数 %.2f 元）。", deductNote, deduct, amount), deduct},
	}
	// 物流延迟话术需要时效占位
	if req.ReasonCat == "logistics_delay" {
		disposals[2].content = fmt.Sprintf(rule.ScriptPrompt, actualRef, strconv.Itoa(promisedH), amount)
	}
	for _, d := range disposals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_return_disposals(doc_no,entry_id,dtype,title,content,amount)
			VALUES($1,$2,$3,$4,$5,$6)`,
			docNo("LD"), entryID, d.dtype, d.title, d.content, d.amount); err != nil {
			writeErr(w, 500, "处置生成失败："+err.Error())
			return
		}
	}

	// 批次质量异常：自动生成 critical 品控事件，衔接既有召回流程
	autoEvent := ""
	if req.ReasonCat == "batch_quality" {
		autoEvent = docNo("EV")
		_, _ = tx.Exec(ctx, `
			INSERT INTO quality_events(event_no,etype,severity,finished_batch_id,order_id,description,status,created_by)
			VALUES($1,'live_return','critical',$2,$3,$4,'open',$5)`,
			autoEvent, finID, orderID,
			fmt.Sprintf("直播集中退货-批次质量异常：%s（退货批次登记 %s，成品 %s）", detail, entryNo, fBatchNo),
			u.ID)
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"entry_no": entryNo, "id": entryID, "amount": amount,
		"disposals":     []string{"refund", "restock", "script", "channel_deduction"},
		"auto_event_no": autoEvent,
	})
}

// ---------- 四项处置执行 ----------

type liveDisposeReq struct {
	DType string `json:"dtype"` // refund / restock / script / channel_deduction
}

// executeLiveDisposal 执行单笔退货的某一项处置（按处置类型校验角色）。
func (s *Server) executeLiveDisposal(w http.ResponseWriter, r *http.Request) {
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req liveDisposeReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "字段错误")
		return
	}
	needRole := map[string][]string{
		"refund":            {"finance", "manager", "admin"},
		"restock":           {"warehouse", "manager", "admin"},
		"script":            {"quality", "manager", "admin"},
		"channel_deduction": {"finance", "manager", "admin"},
	}
	roles, ok := needRole[req.DType]
	if !ok {
		writeErr(w, 400, "dtype 必须为 refund/restock/script/channel_deduction")
		return
	}
	u := currentUser(r)
	allowed := false
	for _, rl := range roles {
		if u.Role == rl {
			allowed = true
		}
	}
	if !allowed {
		writeErr(w, 403, "该处置需要角色："+strings.Join(roles, "/"))
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var dispID int64
	var dispStatus, title string
	var qtyKg float64
	if err := tx.QueryRow(ctx, `
		SELECT d.id,d.status,d.title,d.amount
		FROM live_return_disposals d WHERE d.entry_id=$1 AND d.dtype=$2 FOR UPDATE`,
		entryID, req.DType).Scan(&dispID, &dispStatus, &title, &qtyKg); err != nil {
		writeErr(w, 404, "处置不存在："+req.DType)
		return
	}
	if dispStatus == "done" {
		writeErr(w, 409, "该处置已执行："+title)
		return
	}

	extra := ""
	switch req.DType {
	case "refund":
		// 退款执行后，关联订单转已赔付
		_, _ = tx.Exec(ctx, `
			UPDATE orders SET status='compensated'
			WHERE id=(SELECT order_id FROM live_return_orders WHERE id=$1) AND status='returned'`, entryID)
	case "restock":
		// 库存退回：登记退货回厂（衔接三态处置）+ 入「直播退货仓」隔离
		var finID int64
		var fBatchNo, entryNo string
		var qty float64
		if err := tx.QueryRow(ctx, `
			SELECT b.finished_batch_id,COALESCE(f.batch_no,''),e.entry_no,e.qty_kg
			FROM live_return_orders e
			JOIN live_return_batches b ON b.id=e.batch_id
			LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
			WHERE e.id=$1`, entryID).Scan(&finID, &fBatchNo, &entryNo, &qty); err != nil {
			writeErr(w, 404, "退货登记不存在")
			return
		}
		rtNo := docNo("RT")
		if _, err := tx.Exec(ctx, `
			INSERT INTO return_goods(return_no,finished_batch_id,box_no,qty_kg,note,live_entry_id)
			VALUES($1,$2,$3,$4,$5,$6)`,
			rtNo, finID, "直播退货-"+entryNo, qty,
			"直播集中退货退回（"+entryNo+"），隔离待判", entryID); err != nil {
			writeErr(w, 500, "退货回厂登记失败："+err.Error())
			return
		}
		_, _ = tx.Exec(ctx, `
			INSERT INTO inventory(finished_batch_id,location,qty_kg,status)
			VALUES($1,'直播退货仓',$2,'quarantined')
			ON CONFLICT (finished_batch_id,location,status)
			DO UPDATE SET qty_kg=inventory.qty_kg+EXCLUDED.qty_kg,updated_at=now()`,
			finID, qty)
		extra = "已登记退货回厂 " + rtNo + "，入「直播退货仓」隔离待判"
	}

	if _, err := tx.Exec(ctx, `
		UPDATE live_return_disposals SET status='done',executed_by=$3,executed_at=now()
		WHERE id=$1 AND dtype=$2`, dispID, req.DType, u.Username); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": dispID, "dtype": req.DType, "status": "done", "note": extra})
}

// ---------- 退货结束：三源对比复盘 ----------

// reviewLiveReturn 结束集中退货并生成渠道复盘：
// 对比 主播承诺 / 实际到货时效 / 批次检测 → 渠道扣款 / 质量整改 / 物流追责建议。
func (s *Server) reviewLiveReturn(w http.ResponseWriter, r *http.Request) {
	batchID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u := currentUser(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var st, fBatchNo, product, promiseText string
	var sessionID, finID, channelID int64
	var promisedH int
	if err := tx.QueryRow(ctx, `
		SELECT b.status,b.session_id,b.finished_batch_id,
		       COALESCE(f.batch_no,''),COALESCE(f.product_name,''),
		       ls.channel_id,ls.promised_delivery_hours,COALESCE(ls.promise_text,'')
		FROM live_return_batches b
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.id=$1 FOR UPDATE OF b`, batchID).
		Scan(&st, &sessionID, &finID, &fBatchNo, &product, &channelID, &promisedH, &promiseText); err != nil {
		writeErr(w, 404, "集中退货批次不存在")
		return
	}
	if st != "open" {
		writeErr(w, 409, "该集中退货已复盘结案")
		return
	}

	// 原因分类统计 + 退款合计
	catCnt := map[string]int{}
	catRefund := map[string]float64{}
	crows, _ := tx.Query(ctx, `
		SELECT reason_cat,count(*),COALESCE(sum(amount),0)
		FROM live_return_orders WHERE batch_id=$1 GROUP BY reason_cat`, batchID)
	for crows.Next() {
		var cat string
		var cnt int
		var sum float64
		_ = crows.Scan(&cat, &cnt, &sum)
		catCnt[cat] = cnt
		catRefund[cat] = sum
	}
	crows.Close()

	// 到货时效对比
	var avgH, maxH *float64
	var timedCnt, breachCnt int
	_ = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE actual_delivery_hours IS NOT NULL),
		       count(*) FILTER (WHERE actual_delivery_hours > promised_delivery_hours),
		       avg(actual_delivery_hours),max(actual_delivery_hours)
		FROM live_return_orders WHERE batch_id=$1`, batchID).
		Scan(&timedCnt, &breachCnt, &avgH, &maxH)

	// 批次检测：登记快照 + 留样最新结论
	var sampleUnq, samplePend int
	_ = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE sample_result LIKE '%不合格%' OR sample_result LIKE '%unqualified%'),
		       count(*) FILTER (WHERE sample_result LIKE '%待%' OR sample_result LIKE '%pending%')
		FROM live_return_orders WHERE batch_id=$1`, batchID).Scan(&sampleUnq, &samplePend)
	latestSample := "无留样记录"
	_ = tx.QueryRow(ctx, `
		SELECT sample_no||' '||CASE result WHEN 'qualified' THEN '合格'
		       WHEN 'unqualified' THEN '不合格' ELSE '待检' END
		FROM samples WHERE finished_batch_id=$1
		ORDER BY tested_at DESC NULLS LAST LIMIT 1`, finID).Scan(&latestSample)
	var labUnq int
	_ = tx.QueryRow(ctx,
		`SELECT count(*) FROM samples WHERE finished_batch_id=$1 AND result='unqualified'`, finID).Scan(&labUnq)

	// 渠道扣款合计（处置台账）
	var deductTotal float64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(sum(d.amount),0) FROM live_return_disposals d
		JOIN live_return_orders e ON e.id=d.entry_id
		WHERE e.batch_id=$1 AND d.dtype='channel_deduction'`, batchID).Scan(&deductTotal)
	var refundTotal float64
	_ = tx.QueryRow(ctx,
		`SELECT COALESCE(sum(amount),0) FROM live_return_orders WHERE batch_id=$1`, batchID).Scan(&refundTotal)

	// 建议判定：批次质量 > 主播承诺 > 物流 > 口味 > 无
	suggestion := "none"
	switch {
	case catCnt["batch_quality"] > 0 || sampleUnq > 0 || labUnq > 0:
		suggestion = "quality_rectification"
	case catCnt["promise_mismatch"] > 0:
		suggestion = "channel_deduction"
	case catCnt["logistics_delay"] > 0 || breachCnt > 0:
		suggestion = "logistics_claim"
	case catCnt["taste_problem"] > 0:
		suggestion = "quality_rectification"
	}

	fmtH := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return fmt.Sprintf("%.1f", *v)
	}
	promiseSummary := fmt.Sprintf("主播承诺：%s（承诺到货时效 %d 小时）", promiseText, promisedH)
	deliverySummary := "无到货时效数据"
	if timedCnt > 0 {
		deliverySummary = fmt.Sprintf("实际到货时效均值 %s 小时 / 最高 %s 小时，%d/%d 单超出承诺 %d 小时",
			fmtH(avgH), fmtH(maxH), breachCnt, timedCnt, promisedH)
	}
	sampleSummary := fmt.Sprintf("留样检测：%s；退货登记中批次质量异常 %d 笔、送检不合格 %d 笔",
		latestSample, catCnt["batch_quality"], sampleUnq)

	sugDetail := ""
	restockAdj := "备货策略维持不变"
	switch suggestion {
	case "quality_rectification":
		if catCnt["batch_quality"] > 0 || labUnq > 0 || sampleUnq > 0 {
			sugDetail = fmt.Sprintf("批次质量异常退货 %d 笔（退款 %.2f 元），留样/送检存在不合格；建议对成品 %s（%s）启动质量整改并评估召回，同步复检同线成品。",
				catCnt["batch_quality"], catRefund["batch_quality"], fBatchNo, product)
			restockAdj = fmt.Sprintf("下一场直播暂停「%s」（%s）备货，待质量整改复检合格后恢复；其余品类按本场 80%% 备货。", product, fBatchNo)
		} else {
			sugDetail = fmt.Sprintf("口味问题退货 %d 笔（退款 %.2f 元），建议调味工艺整改并扩大留样感官评测。",
				catCnt["taste_problem"], catRefund["taste_problem"])
			restockAdj = fmt.Sprintf("下一场直播「%s」备货量下调 30%%，调味料工艺整改复检后恢复。", product)
		}
	case "channel_deduction":
		sugDetail = fmt.Sprintf("主播承诺不符退货 %d 笔（退款 %.2f 元），按退款额 100%% 向渠道（主播/机构）扣款 %.2f 元；承诺文案须整改。",
			catCnt["promise_mismatch"], catRefund["promise_mismatch"], deductTotal)
		restockAdj = "下一场直播备货量按本场 -20% 控制，主播承诺文案须经品控审核后上架。"
	case "logistics_claim":
		sugDetail = fmt.Sprintf("物流延迟退货 %d 笔，%d 单实际到货超承诺时效；建议向冷链承运商发起追责索赔，并重新评估时效承诺。",
			catCnt["logistics_delay"], breachCnt)
		restockAdj = fmt.Sprintf("备货量维持，承诺到货时效由 %d 小时调整为 %d 小时，并评估更换冷链承运商。", promisedH, promisedH+24)
	default:
		sugDetail = "各项退货原因均未达处置阈值，无需专项追责。"
	}

	reviewNo := docNo("RV")
	var reviewID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO channel_reviews
		  (review_no,batch_id,channel_id,promise_summary,delivery_summary,sample_summary,
		   suggestion,suggestion_detail,refund_total,deduction_amount,restock_adjustment,reviewed_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		reviewNo, batchID, channelID, promiseSummary, deliverySummary, sampleSummary,
		suggestion, sugDetail, refundTotal, deductTotal, restockAdj, u.ID).Scan(&reviewID); err != nil {
		writeErr(w, 500, "复盘生成失败："+err.Error())
		return
	}

	// 结束退货批次；场次内无其他开放批次则场次转「已复盘」
	_, _ = tx.Exec(ctx, `UPDATE live_return_batches SET status='closed',closed_at=now() WHERE id=$1`, batchID)
	_, _ = tx.Exec(ctx, `
		UPDATE live_sessions SET status='reviewed'
		WHERE id=$1 AND NOT EXISTS(
		  SELECT 1 FROM live_return_batches WHERE session_id=$1 AND status='open' AND id<>$2)`,
		sessionID, batchID)

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"review_no": reviewNo, "id": reviewID, "suggestion": suggestion,
		"refund_total": refundTotal, "deduction_amount": deductTotal,
		"restock_adjustment": restockAdj,
	})
}

// ---------- 渠道复盘：查询 / 同步 ----------

func (s *Server) listChannelReviews(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.pool.Query(r.Context(), `
		SELECT rv.id,rv.review_no,b.batch_no AS return_batch_no,ls.session_no,ls.title AS session_title,
		       ch.name AS channel,rv.suggestion,rv.refund_total,rv.deduction_amount,
		       rv.restock_adjustment,rv.status,rv.reviewed_at::text,rv.synced_at::text
		FROM channel_reviews rv
		JOIN live_return_batches b ON b.id=rv.batch_id
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN channels ch ON ch.id=rv.channel_id
		ORDER BY rv.reviewed_at DESC`)
	defer rows.Close()
	writeJSON(w, 200, scanMaps(rows))
}

func (s *Server) getChannelReview(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	rows, _ := s.pool.Query(r.Context(), `
		SELECT rv.id,rv.review_no,b.batch_no AS return_batch_no,ls.session_no,ls.title AS session_title,
		       ls.anchor,ch.name AS channel,rv.promise_summary,rv.delivery_summary,rv.sample_summary,
		       rv.suggestion,rv.suggestion_detail,rv.refund_total,rv.deduction_amount,
		       rv.restock_adjustment,rv.status,rv.reviewed_at::text,rv.synced_at::text
		FROM channel_reviews rv
		JOIN live_return_batches b ON b.id=rv.batch_id
		JOIN live_sessions ls ON ls.id=b.session_id
		LEFT JOIN channels ch ON ch.id=rv.channel_id
		WHERE rv.id=$1`, id)
	defer rows.Close()
	list := scanMaps(rows)
	if len(list) == 0 {
		writeErr(w, 404, "复盘不存在")
		return
	}
	writeJSON(w, 200, list[0])
}

// syncChannelReview 复盘同步渠道：批次质量异常自动升级品控事件（衔接召回），
// 渠道扣款台账生效，并按成品批次生成「下一场直播备货」建议。
func (s *Server) syncChannelReview(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u := currentUser(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	var status, suggestion, restockAdj, fBatchNo, product, reviewNo string
	var batchID, finID, sessionID int64
	if err := tx.QueryRow(ctx, `
		SELECT rv.status,rv.suggestion,rv.restock_adjustment,rv.batch_id,rv.review_no,
		       b.finished_batch_id,b.session_id,
		       COALESCE(f.batch_no,''),COALESCE(f.product_name,'')
		FROM channel_reviews rv
		JOIN live_return_batches b ON b.id=rv.batch_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE rv.id=$1 FOR UPDATE OF rv`, id).
		Scan(&status, &suggestion, &restockAdj, &batchID, &reviewNo, &finID, &sessionID, &fBatchNo, &product); err != nil {
		writeErr(w, 404, "复盘不存在")
		return
	}
	if status == "synced" {
		writeErr(w, 409, "该复盘已同步渠道")
		return
	}

	if _, err := tx.Exec(ctx,
		`UPDATE channel_reviews SET status='synced',synced_at=now() WHERE id=$1`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	// 渠道扣款台账生效（复盘确认扣款）
	_, _ = tx.Exec(ctx, `
		UPDATE live_return_disposals SET status='done',executed_by='复盘同步',executed_at=now()
		WHERE dtype='channel_deduction' AND status='pending'
		  AND entry_id IN (SELECT id FROM live_return_orders WHERE batch_id=$1)`, batchID)

	// 批次质量异常 → 自动升级 critical 品控事件（尚无开放事件时），衔接召回流程
	autoEvent := ""
	if suggestion == "quality_rectification" {
		var openEv int
		_ = tx.QueryRow(ctx, `
			SELECT count(*) FROM quality_events
			WHERE finished_batch_id=$1 AND etype='live_return' AND status='open'`, finID).Scan(&openEv)
		if openEv == 0 {
			autoEvent = docNo("EV")
			_, _ = tx.Exec(ctx, `
				INSERT INTO quality_events(event_no,etype,severity,finished_batch_id,description,status,created_by)
				VALUES($1,'live_return','critical',$2,$3,'open',$4)`,
				autoEvent, finID,
				fmt.Sprintf("直播集中退货复盘（%s）确认批次质量异常，成品 %s（%s）建议质量整改并评估召回", reviewNo, fBatchNo, product),
				u.ID)
		}
	}

	// 渠道反馈标记已处理
	_, _ = tx.Exec(ctx, `
		UPDATE channel_feedback SET handled=true
		WHERE finished_batch_id=$1 AND ftype IN ('odor','live_return') AND handled=false`, finID)

	// 下一场直播备货建议（按成品批次 upsert 最新一条）
	direction, pct := "hold", 0
	switch suggestion {
	case "quality_rectification":
		direction, pct = "pause", 100
	case "channel_deduction":
		direction, pct = "down", 20
	case "logistics_claim":
		direction, pct = "hold", 0
	}
	_, _ = tx.Exec(ctx, `
		INSERT INTO live_restock_advice(finished_batch_id,session_id,review_id,direction,adjust_pct,advice)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (finished_batch_id)
		DO UPDATE SET session_id=EXCLUDED.session_id,review_id=EXCLUDED.review_id,
		  direction=EXCLUDED.direction,adjust_pct=EXCLUDED.adjust_pct,
		  advice=EXCLUDED.advice,updated_at=now()`,
		finID, sessionID, id, direction, pct, restockAdj)

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": id, "status": "synced", "auto_event_no": autoEvent,
		"restock_adjustment": restockAdj,
	})
}

// ---------- 下一场直播备货建议 ----------

func (s *Server) listRestockAdvice(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.pool.Query(r.Context(), `
		SELECT a.id,f.batch_no AS finished_batch_no,f.product_name,ls.session_no,ls.title AS session_title,
		       rv.review_no,a.direction,a.adjust_pct,a.advice,a.updated_at::text
		FROM live_restock_advice a
		JOIN finished_batches f ON f.id=a.finished_batch_id
		LEFT JOIN live_sessions ls ON ls.id=a.session_id
		LEFT JOIN channel_reviews rv ON rv.id=a.review_id
		ORDER BY a.updated_at DESC`)
	defer rows.Close()
	writeJSON(w, 200, scanMaps(rows))
}
