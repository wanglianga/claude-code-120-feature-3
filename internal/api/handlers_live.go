package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// 集中退货四类原因
const (
	reasonPromise = "promise_mismatch" // 主播承诺不符
	reasonLogi    = "logistics_delay"  // 物流延迟
	reasonTaste   = "taste_issue"      // 口味问题
	reasonBatch   = "batch_quality"    // 批次质量异常
)

var liveReasonLabel = map[string]string{
	reasonPromise: "主播承诺不符",
	reasonLogi:    "物流延迟",
	reasonTaste:   "口味问题",
	reasonBatch:   "批次质量异常",
}

// 各原因对应的渠道扣款类型与承担比例（占退款金额）
var liveChargeRule = map[string]struct {
	chargeType string
	label      string
	rate       float64
}{
	reasonPromise: {"commitment_mismatch", "主播承诺不符扣款", 1.0},
	reasonLogi:    {"logistics_breach", "物流时效违约扣款", 0.8},
	reasonTaste:   {"service_share", "客诉服务分担扣款", 0.2},
	reasonBatch:   {"quality_penalty", "批次质量违约金", 0.5},
}

var chargeTypeLabel = map[string]string{
	"commitment_mismatch": "主播承诺不符扣款",
	"logistics_breach":    "物流时效违约扣款",
	"quality_penalty":     "批次质量违约金",
	"service_share":       "客诉服务分担扣款",
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ---------- 集中退货登记 ----------

type liveItemReq struct {
	OrderNo       string   `json:"order_no"`
	FinishedBatch string   `json:"finished_batch_no"`
	BoxNo         string   `json:"box_no"`
	Qty           *float64 `json:"qty"`
	QtyKg         *float64 `json:"qty_kg"`
	Reason        string   `json:"reason"`
	PromisedHours *float64 `json:"promised_arrival_hours"`
	ActualHours   *float64 `json:"actual_arrival_hours"`
	QualityResult string   `json:"quality_result"`
	Evidence      string   `json:"evidence"`
}

type liveReturnReq struct {
	ReturnNo           string        `json:"return_no"`
	LiveSession        string        `json:"live_session"`
	ChannelCode        string        `json:"channel_code"`
	AnchorName         string        `json:"anchor_name"`
	FinishedBatchNo    string        `json:"finished_batch_no"`
	PromisedArrivalHrs *float64      `json:"promised_arrival_hours"`
	PromisedQuality    string        `json:"promised_quality"`
	PromisedShelfLife  string        `json:"promised_shelf_life"`
	UnitPrice          float64       `json:"unit_price"`
	Note               string        `json:"note"`
	Items              []liveItemReq `json:"items"`
}

// createLiveReturn 一场直播的集中退货：逐单归入四类原因，并分别生成
// 退款单 / 库存退回单 / 客服话术 / 渠道扣款单。
func (s *Server) createLiveReturn(w http.ResponseWriter, r *http.Request) {
	var req liveReturnReq
	if err := decode(r, &req); err != nil || req.LiveSession == "" || len(req.Items) == 0 {
		writeErr(w, 400, "缺少 live_session/items")
		return
	}
	if req.ChannelCode == "" {
		req.ChannelCode = "CH03"
	}
	if req.UnitPrice <= 0 {
		req.UnitPrice = 59.9
	}
	for _, it := range req.Items {
		if _, ok := liveReasonLabel[it.Reason]; !ok {
			writeErr(w, 400, "明细原因必须为 promise_mismatch/logistics_delay/taste_issue/batch_quality")
			return
		}
		if it.QualityResult != "" && it.QualityResult != "qualified" && it.QualityResult != "unqualified" {
			writeErr(w, 400, "quality_result 必须为 qualified/unqualified")
			return
		}
	}
	u := currentUser(r)
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	// 1) 退货批次主单（留存主播承诺快照）
	no := req.ReturnNo
	if no == "" {
		no = docNo("LR")
	}
	var lrID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO live_return_batches
		  (return_no,live_session,channel_id,anchor_name,finished_batch_id,
		   promised_arrival_hours,promised_quality,promised_shelf_life,created_by,note)
		SELECT $1,$2,id,$3,(SELECT id FROM finished_batches WHERE batch_no=$4),
		       $5,$6,$7,$8,$9
		FROM channels WHERE code=$10
		RETURNING id`,
		no, req.LiveSession, req.AnchorName, req.FinishedBatchNo,
		req.PromisedArrivalHrs, req.PromisedQuality, req.PromisedShelfLife, u.ID, req.Note,
		req.ChannelCode).Scan(&lrID); err != nil {
		writeErr(w, 400, "直播退货批次创建失败："+err.Error())
		return
	}

	var headerFinID *int64
	if req.FinishedBatchNo != "" {
		_ = tx.QueryRow(ctx, `SELECT id FROM finished_batches WHERE batch_no=$1`, req.FinishedBatchNo).Scan(&headerFinID)
	}

	reasonCount := map[string]int{}
	reasonKg := map[string]float64{}
	refundTotal, chargeTotal, stockTotal := 0.0, 0.0, 0.0
	batchQuality := false
	var lateOrders []string

	for _, it := range req.Items {
		// 解析订单 / 成品
		var orderID, finID *int64
		if it.OrderNo != "" {
			_ = tx.QueryRow(ctx, `SELECT id FROM orders WHERE order_no=$1`, it.OrderNo).Scan(&orderID)
		}
		if it.FinishedBatch != "" {
			_ = tx.QueryRow(ctx, `SELECT id FROM finished_batches WHERE batch_no=$1`, it.FinishedBatch).Scan(&finID)
		}
		if finID == nil {
			finID = headerFinID
		}
		qty := 0.0
		if it.Qty != nil {
			qty = *it.Qty
		}
		qtyKg := 0.0
		if it.QtyKg != nil {
			qtyKg = *it.QtyKg
		}

		// 明细承诺/实际时效：缺省继承批次承诺
		promised := it.PromisedHours
		if promised == nil {
			promised = req.PromisedArrivalHrs
		}
		if it.ActualHours != nil && promised != nil && *it.ActualHours > *promised {
			lateOrders = append(lateOrders, it.OrderNo)
		}

		var itemID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO live_return_items
			  (live_return_id,order_id,finished_batch_id,box_no,qty,reason,
			   promised_arrival_hours,actual_arrival_hours,quality_result,evidence)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10) RETURNING id`,
			lrID, orderID, finID, it.BoxNo, qty, it.Reason,
			promised, it.ActualHours, it.QualityResult, it.Evidence).Scan(&itemID); err != nil {
			writeErr(w, 400, "退货明细写入失败："+err.Error())
			return
		}

		// ① 退款单（集中退货全额退，按盒数×单价）
		amount := round2(qty * req.UnitPrice)
		refundTotal += amount
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_refunds(refund_no,live_return_id,item_id,order_id,amount,reason)
			VALUES($1,$2,$3,$4,$5,$6)`,
			docNo("LRF"), lrID, itemID, orderID, amount,
			"直播集中退货-"+liveReasonLabel[it.Reason]); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		// ② 库存退回：按原因给建议处置，并落一条待判 return_goods（实物回厂后由品控三态判定）
		suggest, loc, stockNote := stockSuggestion(it.Reason, it.QualityResult)
		var rgID *int64
		if finID != nil && qtyKg > 0 {
			rgNo := docNo("RT")
			if err := tx.QueryRow(ctx, `
				INSERT INTO return_goods(return_no,finished_batch_id,box_no,qty_kg,note)
				VALUES($1,$2,$3,$4,$5) RETURNING id`,
				rgNo, *finID, it.BoxNo, qtyKg,
				fmt.Sprintf("直播集中退货 %s（%s），建议：%s，待回厂品控判定", no, liveReasonLabel[it.Reason],
					map[string]string{"reinspect": "可复检", "rework": "可返工", "destroy": "必须销毁"}[suggest])).
				Scan(&rgID); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		stockTotal += qtyKg
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_stock_returns
			  (stock_no,live_return_id,item_id,return_goods_id,finished_batch_id,box_no,qty_kg,
			   suggested_disposition,target_location,note)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			docNo("LRS"), lrID, itemID, rgID, finID, it.BoxNo, qtyKg, suggest, loc, stockNote); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		// ③ 客服话术（按原因分别生成）
		title, content := csScript(it.Reason, req, it, amount, promised)
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_cs_scripts(script_no,live_return_id,item_id,reason,audience,title,content)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			docNo("LCS"), lrID, itemID, it.Reason, "直播间下单顾客", title, content); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		// ④ 渠道扣款（按原因适用扣款类型与比例）
		rule := liveChargeRule[it.Reason]
		charge := round2(amount * rule.rate)
		chargeTotal += charge
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_channel_charges
			  (charge_no,live_return_id,item_id,channel_id,amount,charge_type,reason,status)
			VALUES($1,$2,$3,(SELECT channel_id FROM live_return_batches WHERE id=$2),$4,$5,$6,'pending')`,
			docNo("LCC"), lrID, itemID, charge, rule.chargeType,
			fmt.Sprintf("%s｜场次 %s｜订单 %s｜退款 %.2f×%d%%", rule.label, req.LiveSession, it.OrderNo, amount, int(rule.rate*100))); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		// 订单状态同步为已退货（召回/已赔付终态不回退）
		if orderID != nil {
			_, _ = tx.Exec(ctx, `UPDATE orders SET status='returned' WHERE id=$1 AND status IN ('sold','recalled')`, *orderID)
		}
		reasonCount[it.Reason]++
		reasonKg[it.Reason] += qtyKg
		if it.Reason == reasonBatch || it.QualityResult == "unqualified" {
			batchQuality = true
		}
	}

	// 批次质量异常：自动生成 critical 品控事件，可衔接现有召回闭环
	autoEventNo := ""
	if batchQuality && headerFinID != nil {
		autoEventNo = docNo("EV")
		_, _ = tx.Exec(ctx, `
			INSERT INTO quality_events(event_no,etype,severity,finished_batch_id,description,status,created_by)
			VALUES($1,'live_return','critical',$2,$3,'open',$4)`,
			autoEventNo, *headerFinID,
			fmt.Sprintf("直播间 %s 集中退货出现批次质量异常：%s", req.LiveSession, no), u.ID)
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	reasons := map[string]any{}
	for k, l := range liveReasonLabel {
		reasons[k] = map[string]any{"label": l, "count": reasonCount[k], "qty_kg": round2(reasonKg[k])}
	}
	writeJSON(w, 201, map[string]any{
		"id": lrID, "return_no": no, "items": len(req.Items),
		"refund_total": round2(refundTotal), "charge_total": round2(chargeTotal),
		"stock_return_kg": round2(stockTotal), "late_orders": len(lateOrders),
		"by_reason": reasons, "auto_event_no": autoEventNo,
	})
}

// stockSuggestion 按退货原因 + 到厂质量结论给出库存建议处置
func stockSuggestion(reason, qualityResult string) (disposition, location, note string) {
	switch reason {
	case reasonPromise:
		return "reinspect", "复检待判区", "商品本身无质量投诉，复检合格后可再售"
	case reasonLogi:
		if qualityResult == "unqualified" {
			return "destroy", "销毁区", "超时运输且已变质/异味，必须销毁"
		}
		return "reinspect", "复检待判区", "时效违约但温度记录/感官待复检确认"
	case reasonTaste:
		return "rework", "返工区", "口味偏差可重新调味/包覆返工"
	case reasonBatch:
		if qualityResult == "qualified" {
			return "reinspect", "复检待判区", "批次异常预警但本单初检合格，隔离待整批复检"
		}
		return "destroy", "销毁区", "批次质量异常，禁止再售/返工，必须销毁"
	}
	return "reinspect", "复检待判区", ""
}

// csScript 按四类原因分别生成客服话术
func csScript(reason string, h liveReturnReq, it liveItemReq, amount float64, promised *float64) (title, content string) {
	order := it.OrderNo
	if order == "" {
		order = it.BoxNo
	}
	switch reason {
	case reasonPromise:
		return "主播承诺不符 · 致歉与全额退款话术",
			fmt.Sprintf(
				"您好，我是海鲜专场客服。关于您在「%s」（主播：%s）下单的订单 %s，经核对直播口播与实物，确实存在承诺与实物不符的情况，给您造成了误导，我们郑重致歉。\n"+
					"本场承诺：%s。现已为您发起全额退款 %.2f 元（1-3 个工作日原路退回），商品由冷链上门取回，复检后不会再作原单二次销售。\n"+
					"我们已对该场直播话术与选品开展核查，后续将加强口播审核，感谢您的监督。",
				h.LiveSession, firstNonEmpty(h.AnchorName, "本场主播"), order, firstNonEmpty(h.PromisedQuality, "商品规格/品质"), amount)
	case reasonLogi:
		return "物流延迟 · 致歉与运费补偿话术",
			fmt.Sprintf(
				"您好，关于订单 %s 的物流延迟问题向您致歉。本场承诺 %v 小时送达，您实际 %v 小时收到，已超出承诺时效。\n"+
					"我们为您办理全额退款 %.2f 元，并同步调取该路由冷链温度记录追责承运方。因延迟导致商品状态异常的，请勿食用，我们安排销毁并补偿。\n"+
					"后续该场次将收缩远区可售范围、切换冷链时效更快的线路。",
				order, derefF(promised), derefF(it.ActualHours), amount)
	case reasonTaste:
		return "口味问题 · 解释与换货/返工话术",
			fmt.Sprintf(
				"您好，很抱歉订单 %s 的口味没有达到您的预期。海鲜预制菜口味存在个体差异，您反馈的口感问题我们已完整记录并转交研发。\n"+
					"本单为您办理退款 %.2f 元（也可选择更换口味）；退回商品将在返工区重新调味评估，不会原样再发。\n"+
					"下一场直播我们会调整该品配方并在详情页更客观标注风味。",
				order, amount)
	case reasonBatch:
		return "批次质量异常 · 紧急停售与召回告知话术",
			fmt.Sprintf(
				"您好，我是品控客服。您订单 %s 反馈的异常经研判属于批次质量风险，我们已启动紧急流程：该成品批次立即停售、在库隔离，并登记品控事件评估召回。\n"+
					"请您暂停食用、保留包装与商品并拍照（%s），我们安排全额退款 %.2f 元并按召回政策处理赔付。\n"+
					"批次留样复检结论出来后会第一时间同步给您，由此带来的不便深表歉意。",
				order, firstNonEmpty(it.Evidence, "凭证待补充"), amount)
	}
	return "", ""
}

// 辅助：明细缺省承诺时效取批次承诺
func derefF(v *float64) any {
	if v == nil {
		return "—"
	}
	return *v
}

// ---------- 列表 / 详情 ----------

func (s *Server) listLiveReturns(w http.ResponseWriter, r *http.Request) {
	rows, _ := s.pool.Query(r.Context(), `
		SELECT b.id,b.return_no,b.live_session,ch.code AS channel_code,ch.name AS channel,
		       b.anchor_name,f.batch_no AS finished_batch_no,
		       b.promised_arrival_hours,b.promised_quality,b.status,
		       b.returned_at::text,
		       (SELECT count(*) FROM live_return_items i WHERE i.live_return_id=b.id) AS items,
		       (SELECT COALESCE(sum(amount),0) FROM live_refunds x WHERE x.live_return_id=b.id) AS refund_total,
		       (SELECT COALESCE(sum(amount),0) FROM live_channel_charges x WHERE x.live_return_id=b.id) AS charge_total,
		       (SELECT COALESCE(sum(qty_kg),0) FROM live_stock_returns x WHERE x.live_return_id=b.id) AS returned_kg,
		       (SELECT count(*) FROM live_return_items i WHERE i.live_return_id=b.id AND i.reason='promise_mismatch') AS r_promise,
		       (SELECT count(*) FROM live_return_items i WHERE i.live_return_id=b.id AND i.reason='logistics_delay') AS r_logi,
		       (SELECT count(*) FROM live_return_items i WHERE i.live_return_id=b.id AND i.reason='taste_issue') AS r_taste,
		       (SELECT count(*) FROM live_return_items i WHERE i.live_return_id=b.id AND i.reason='batch_quality') AS r_batch
		FROM live_return_batches b
		LEFT JOIN channels ch ON ch.id=b.channel_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		ORDER BY b.returned_at DESC`)
	defer rows.Close()
	writeJSON(w, 200, scanMaps(rows))
}

func (s *Server) getLiveReturn(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ctx := r.Context()

	headRows, err := s.pool.Query(ctx, `
		SELECT b.id,b.return_no,b.live_session,ch.code AS channel_code,ch.name AS channel,
		       b.anchor_name,f.batch_no AS finished_batch_no,f.product_name,
		       b.promised_arrival_hours,b.promised_quality,b.promised_shelf_life,
		       b.status,b.returned_at::text,b.closed_at::text,b.note
		FROM live_return_batches b
		LEFT JOIN channels ch ON ch.id=b.channel_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.id=$1`, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	head := scanMaps(headRows)
	headRows.Close()
	if len(head) == 0 {
		writeErr(w, 404, "直播退货批次不存在")
		return
	}

	qm := func(sql string) []map[string]any {
		rows, qerr := s.pool.Query(ctx, sql, id)
		if qerr != nil {
			return []map[string]any{}
		}
		defer rows.Close()
		return scanMaps(rows)
	}

	items := qm(`
		SELECT i.id,o.order_no,f.batch_no AS finished_batch_no,i.box_no,i.qty,x.qty_kg,
		       i.reason,i.promised_arrival_hours,i.actual_arrival_hours,i.quality_result,i.evidence
		FROM live_return_items i
		LEFT JOIN orders o ON o.id=i.order_id
		LEFT JOIN finished_batches f ON f.id=i.finished_batch_id
		LEFT JOIN LATERAL (
		  SELECT qty_kg FROM live_stock_returns s WHERE s.item_id=i.id LIMIT 1
		) x ON true
		WHERE i.live_return_id=$1 ORDER BY i.id`)

	refunds := qm(`
		SELECT rf.id,rf.refund_no,o.order_no,rf.amount,rf.reason,rf.status,rf.refunded_at::text
		FROM live_refunds rf LEFT JOIN orders o ON o.id=rf.order_id
		WHERE rf.live_return_id=$1 ORDER BY rf.id`)

	stocks := qm(`
		SELECT st.stock_no,rg.return_no AS return_goods_no,f.batch_no AS finished_batch_no,
		       st.box_no,st.qty_kg,st.suggested_disposition,st.target_location,st.note
		FROM live_stock_returns st
		LEFT JOIN return_goods rg ON rg.id=st.return_goods_id
		LEFT JOIN finished_batches f ON f.id=st.finished_batch_id
		WHERE st.live_return_id=$1 ORDER BY st.id`)

	scripts := qm(`
		SELECT script_no,reason,audience,title,content FROM live_cs_scripts
		WHERE live_return_id=$1 ORDER BY id`)

	charges := qm(`
		SELECT c.id,c.charge_no,ch.name AS channel,c.amount,c.charge_type,c.reason,c.status,c.deducted_at::text
		FROM live_channel_charges c LEFT JOIN channels ch ON ch.id=c.channel_id
		WHERE c.live_return_id=$1 ORDER BY c.id`)

	review := qm(`SELECT promise_compare,logistics_compare,quality_compare,summary,reviewed_at::text
		FROM live_return_reviews WHERE live_return_id=$1`)
	suggestions := qm(`SELECT stype,title,content,amount,status FROM live_review_suggestions
		WHERE live_return_id=$1 ORDER BY id`)
	syncs := qm(`SELECT id,sync_no,content,status,response,synced_at::text FROM channel_review_sync
		WHERE live_return_id=$1 ORDER BY id`)
	restocks := qm(`SELECT product_name,shipped_kg,returned_kg,return_rate,suggest_qty_kg,reason
		FROM live_restock_advice WHERE live_return_id=$1 ORDER BY id`)

	writeJSON(w, 200, map[string]any{
		"batch": head[0], "items": items, "refunds": refunds, "stocks": stocks,
		"scripts": scripts, "charges": charges,
		"review":      review,
		"suggestions": suggestions, "syncs": syncs, "restocks": restocks,
	})
}

// ---------- 退款支付 / 渠道扣款状态（财务落实） ----------

func (s *Server) payLiveRefund(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE live_refunds SET status='refunded',refunded_at=now() WHERE id=$1 AND status='pending'`, id)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "退款单不存在或已退款")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": "refunded"})
}

func (s *Server) updateLiveCharge(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		Status string `json:"status"`
	}
	if err := decode(r, &req); err != nil ||
		(req.Status != "confirmed" && req.Status != "deducted" && req.Status != "pending") {
		writeErr(w, 400, "status 必须为 pending/confirmed/deducted")
		return
	}
	setDed := ""
	if req.Status == "deducted" {
		setDed = ",deducted_at=now()"
	}
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE live_channel_charges SET status=$2`+setDed+` WHERE id=$1`, id, req.Status)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "渠道扣款单不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": req.Status})
}
