package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// reviewLiveReturn 直播退货复盘：对比主播承诺、实际到货时效、批次检测，
// 形成渠道扣款 / 质量整改 / 物流追责三类建议，并给出下一场直播备货建议。
func (s *Server) reviewLiveReturn(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u := currentUser(r)
	ctx := r.Context()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)

	// 0) 锁定批次并校验（先锁主表，再取关联信息，避免 FOR UPDATE 用于外连接 nullable 侧）
	var chID *int64
	var chName, session, anchor, promisedQuality, shelfLife, finBatchNo string
	var promisedHrs *float64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status,live_session,COALESCE(anchor_name,''),
		       COALESCE(promised_quality,''),COALESCE(promised_shelf_life,''),
		       promised_arrival_hours,channel_id
		FROM live_return_batches WHERE id=$1 FOR UPDATE`, id).
		Scan(&status, &session, &anchor, &promisedQuality, &shelfLife, &promisedHrs, &chID); err != nil {
		writeErr(w, 404, "直播退货批次不存在")
		return
	}
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(ch.name,''),COALESCE(f.batch_no,'')
		FROM live_return_batches b
		LEFT JOIN channels ch ON ch.id=b.channel_id
		LEFT JOIN finished_batches f ON f.id=b.finished_batch_id
		WHERE b.id=$1`, id).Scan(&chName, &finBatchNo)
	var existed string
	_ = tx.QueryRow(ctx, `SELECT 'x' FROM live_return_reviews WHERE live_return_id=$1`, id).Scan(&existed)
	if existed != "" {
		writeErr(w, 409, "该场直播退货已复盘，结论留档不可重复生成")
		return
	}

	// ---- 维度一：主播承诺对比 ----
	var promiseCnt int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM live_return_items
		WHERE live_return_id=$1 AND reason='promise_mismatch'`, id).Scan(&promiseCnt)
	promiseTxt := fmt.Sprintf(
		"主播（%s）在「%s」口播承诺：%s；保鲜承诺：%s。本场因「主播承诺不符」退货 %d 单，认定直播话术与实物存在偏差。",
		anchor, session, na(promisedQuality), na(shelfLife), promiseCnt)
	if promisedHrs != nil {
		promiseTxt += fmt.Sprintf(" 承诺 %.0f 小时送达。", *promisedHrs)
	}
	if promiseCnt == 0 {
		promiseTxt = fmt.Sprintf("主播（%s）口播承诺（%s；%s）与本批退货无直接不符项，承诺维度暂未发现违规。",
			anchor, na(promisedQuality), na(shelfLife))
	}

	// ---- 维度二：实际到货时效对比 ----
	type delay struct {
		orderNo string
		p, a    float64
	}
	var delays []delay
	var lateCnt int
	var sumDelay float64
	drows, _ := tx.Query(ctx, `
		SELECT COALESCE(o.order_no,i.box_no,'-'),i.promised_arrival_hours,i.actual_arrival_hours
		FROM live_return_items i LEFT JOIN orders o ON o.id=i.order_id
		WHERE i.live_return_id=$1 AND i.actual_arrival_hours IS NOT NULL AND i.promised_arrival_hours IS NOT NULL`, id)
	for drows.Next() {
		var on string
		var p, a float64
		_ = drows.Scan(&on, &p, &a)
		if a > p {
			lateCnt++
			sumDelay += a - p
			delays = append(delays, delay{on, p, a})
		}
	}
	drows.Close()
	var logiCnt int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM live_return_items
		WHERE live_return_id=$1 AND reason='logistics_delay'`, id).Scan(&logiCnt)
	logiTxt := "本场退货无在途时效数据，无法核算到货时效偏差。"
	if len(delays) > 0 {
		avg := sumDelay / float64(lateCnt)
		worst := delays[0]
		for _, d := range delays[1:] {
			if d.a-d.p > worst.a-worst.p {
				worst = d
			}
		}
		logiTxt = fmt.Sprintf(
			"共 %d 单超时送达（物流延迟原因退货 %d 单），平均超时 %.1f 小时；最严重订单 %s 承诺 %.0fh/实际 %.0fh（超时 %.0fh）。",
			lateCnt, logiCnt, avg, worst.orderNo, worst.p, worst.a, worst.a-worst.p)
	}

	// ---- 维度三：批次留样检测对比 ----
	var batchCnt, unqualItems int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM live_return_items
		WHERE live_return_id=$1 AND reason='batch_quality'`, id).Scan(&batchCnt)
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM live_return_items
		WHERE live_return_id=$1 AND quality_result='unqualified'`, id).Scan(&unqualItems)

	type sampleInfo struct {
		sampleNo, result, labNote string
	}
	var samples []sampleInfo
	srows, _ := tx.Query(ctx, `
		SELECT sm.sample_no,sm.result,COALESCE(sm.lab_note,'')
		FROM samples sm
		WHERE sm.finished_batch_id=(SELECT finished_batch_id FROM live_return_batches WHERE id=$1)
		ORDER BY sm.taken_at`, id)
	for srows.Next() {
		var x sampleInfo
		_ = srows.Scan(&x.sampleNo, &x.result, &x.labNote)
		samples = append(samples, x)
	}
	srows.Close()

	// 批次链路：原料入库温度 / 解冻时长
	type chainInfo struct {
		rawBatch, supplier string
		inTemp             float64
		thawMin            *int
	}
	var chains []chainInfo
	crows, _ := tx.Query(ctx, `
		SELECT DISTINCT rm.batch_no,COALESCE(sp.name,''),rm.inbound_temp_c,t.duration_minutes
		FROM batch_links l
		JOIN raw_material_batches rm ON rm.id=l.raw_batch_id
		LEFT JOIN suppliers sp ON sp.id=rm.supplier_id
		LEFT JOIN thaw_records t ON t.raw_batch_id=rm.id
		WHERE l.finished_batch_id=(SELECT finished_batch_id FROM live_return_batches WHERE id=$1)`, id)
	for crows.Next() {
		var c chainInfo
		_ = crows.Scan(&c.rawBatch, &c.supplier, &c.inTemp, &c.thawMin)
		chains = append(chains, c)
	}
	crows.Close()

	qualTxt := fmt.Sprintf("批次质量异常退货 %d 单、到厂判不合格 %d 单。", batchCnt, unqualItems)
	if len(samples) > 0 {
		for _, sm := range samples {
			resName := map[string]string{"qualified": "合格", "unqualified": "不合格", "pending": "待检"}[sm.result]
			qualTxt += fmt.Sprintf("留样 %s 结论：%s（%s）。", sm.sampleNo, resName, sm.labNote)
		}
	} else {
		qualTxt += "该成品批次暂无留样记录，建议立即补检。"
	}
	for _, c := range chains {
		qualTxt += fmt.Sprintf(" 原料 %s（%s）入库 %.2f℃", c.rawBatch, c.supplier, c.inTemp)
		if c.inTemp > -18 {
			qualTxt += "【超 -18℃ 限值】"
		}
		if c.thawMin != nil {
			qualTxt += fmt.Sprintf("，解冻 %d 分钟", *c.thawMin)
			if *c.thawMin > 240 {
				qualTxt += "【超 240 分钟限值】"
			}
		}
		qualTxt += "。"
	}

	// ---- 生成三类建议 ----
	var sugSummary []string

	// ① 渠道扣款建议：承诺不符 + 批次质量违约金，引用已生成渠道扣款单金额
	var deductAmount float64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(sum(amount),0) FROM live_channel_charges
		WHERE live_return_id=$1 AND charge_type IN ('commitment_mismatch','quality_penalty')`, id).Scan(&deductAmount)
	if promiseCnt > 0 || batchCnt > 0 || deductAmount > 0 {
		title := "渠道扣款：主播承诺不符 / 批次质量违约"
		content := fmt.Sprintf(
			"建议向渠道「%s」发起对账扣款 %.2f 元：其中承诺不符 %d 单由渠道与主播团队全额承担，批次质量 %d 单按违约金 50%% 计；要求渠道提交主播口播整改承诺。",
			chName, deductAmount, promiseCnt, batchCnt)
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_review_suggestions(live_return_id,stype,title,content,amount)
			VALUES($1,'channel_deduction',$2,$3,$4)`, id, title, content, deductAmount); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		sugSummary = append(sugSummary, title)
	}

	// ② 质量整改建议
	if batchCnt > 0 || unqualItems > 0 {
		title := "质量整改：同线原料/解冻/留样全链复核"
		content := "建议立即对涉事成品批次在库库存隔离复检，同批原料暂停投用；"
		var rawFlags []string
		for _, c := range chains {
			var flags []string
			if c.inTemp > -18 {
				flags = append(flags, "入库温度超标")
			}
			if c.thawMin != nil && *c.thawMin > 240 {
				flags = append(flags, "解冻超时")
			}
			if len(flags) > 0 {
				rawFlags = append(rawFlags, fmt.Sprintf("原料 %s（%s）", c.rawBatch, strings.Join(flags, "、")))
			}
		}
		if len(rawFlags) > 0 {
			content += strings.Join(rawFlags, "；") + "；"
		}
		content += "复盘解冻间中心温度与用水批次、金检/包装记录，留样加做微生物与挥发性盐基氮检测，整改通过前该品不得排播。"
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_review_suggestions(live_return_id,stype,title,content,amount)
			VALUES($1,'quality_rectification',$2,$3,0)`, id, title, content); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		sugSummary = append(sugSummary, title)
	}

	// ③ 物流追责建议
	var logiAmount float64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(sum(amount),0) FROM live_channel_charges
		WHERE live_return_id=$1 AND charge_type='logistics_breach'`, id).Scan(&logiAmount)
	if lateCnt > 0 || logiCnt > 0 {
		title := "物流追责：超时路由冷链索赔与线路调整"
		content := fmt.Sprintf(
			"%s 建议凭签收时间与冷链温度记录向承运/渠道物流发起追责索赔 %.2f 元，暂停该超时路由的直播可售区域，要求出具时效整改方案。",
			logiTxt, logiAmount)
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_review_suggestions(live_return_id,stype,title,content,amount)
			VALUES($1,'logistics_accountability',$2,$3,$4)`, id, title, content, logiAmount); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		sugSummary = append(sugSummary, title)
	}

	// 无任何问题维度时给一条观察建议，保证复盘结论完整
	if len(sugSummary) == 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO live_review_suggestions(live_return_id,stype,title,content,amount)
			VALUES($1,'quality_rectification','持续观察：维持现行质控与排播',
				'本场退货未发现承诺违规、系统性时效超时或批次质量异常，维持现行质控标准，关注口味反馈趋势即可。',0)`, id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}

	summary := fmt.Sprintf("「%s」集中退货复盘完成：%s %s 质量维度——%s",
		session, promiseTxt, logiTxt, qualTxt)

	if _, err := tx.Exec(ctx, `
		INSERT INTO live_return_reviews
		  (live_return_id,promise_compare,logistics_compare,quality_compare,summary,reviewed_by)
		VALUES($1,$2,$3,$4,$5,$6)`,
		id, promiseTxt, logiTxt, qualTxt, summary, u.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE live_return_batches SET status='reviewed' WHERE id=$1`, id)

	// ---- 复盘结果影响下一场直播备货 ----
	restock, err := buildRestockAdvice(ctx, tx, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": id, "status": "reviewed",
		"promise_compare": promiseTxt, "logistics_compare": logiTxt,
		"quality_compare": qualTxt, "suggestions_count": len(sugSummary),
		"restock_advice": restock,
	})
}

// buildRestockAdvice 依据复盘结论计算下一场直播备货建议（按涉事成品维度），
// 写入 live_restock_advice 并返回摘要。规则（从严叠加）：
//
//	批次质量异常/到厂不合格 → 下场停播该品（建议 0，整改通过前不得排播）
//	主播承诺不符           → 压减至基准 70%
//	物流延迟               → 压减至基准 80% 并收缩远区
//	口味问题               → 压减至基准 90% 并调整配方
//
// 基准备货量 = 该渠道该成品本场发货量 − 本场退回量。
func buildRestockAdvice(ctx context.Context, tx pgx.Tx, liveReturnID int64) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT i.finished_batch_id,COALESCE(f.product_name,''),
		       COALESCE(sum(st.qty_kg),0),
		       count(*) FILTER (WHERE i.reason='promise_mismatch'),
		       count(*) FILTER (WHERE i.reason='logistics_delay'),
		       count(*) FILTER (WHERE i.reason='taste_issue'),
		       count(*) FILTER (WHERE i.reason='batch_quality'),
		       count(*) FILTER (WHERE i.quality_result='unqualified')
		FROM live_return_items i
		JOIN live_stock_returns st ON st.item_id=i.id
		LEFT JOIN finished_batches f ON f.id=i.finished_batch_id
		WHERE i.live_return_id=$1 AND i.finished_batch_id IS NOT NULL
		GROUP BY i.finished_batch_id,f.product_name`, liveReturnID)
	if err != nil {
		return nil, err
	}
	type agg struct {
		finID                                 int64
		product                               string
		returnedKg                            float64
		promiseN, logiN, tasteN, batchN, unqN int
	}
	var aggs []agg
	for rows.Next() {
		var a agg
		if err := rows.Scan(&a.finID, &a.product, &a.returnedKg,
			&a.promiseN, &a.logiN, &a.tasteN, &a.batchN, &a.unqN); err != nil {
			rows.Close()
			return nil, err
		}
		aggs = append(aggs, a)
	}
	rows.Close()

	out := []map[string]any{}
	for _, a := range aggs {
		// 该渠道（本场直播渠道）该成品历史发货量
		var shippedKg float64
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(sum(si.qty_kg),0)
			FROM shipment_items si
			JOIN shipments sh ON sh.id=si.shipment_id
			WHERE si.finished_batch_id=$1
			  AND sh.channel_id=(SELECT channel_id FROM live_return_batches WHERE id=$2)`,
			a.finID, liveReturnID).Scan(&shippedKg)
		if shippedKg <= 0 {
			// 渠道维度无记录时退回成品总产量作为基准
			_ = tx.QueryRow(ctx, `SELECT COALESCE(qty_kg,0) FROM finished_batches WHERE id=$1`, a.finID).Scan(&shippedKg)
		}
		rate := 0.0
		if shippedKg > 0 {
			rate = round2(a.returnedKg / shippedKg * 100)
		}
		base := shippedKg - a.returnedKg
		if base < 0 {
			base = 0
		}

		factor := 1.0
		reasons := []string{}
		switch {
		case a.batchN > 0 || a.unqN > 0:
			factor = 0
			reasons = append(reasons, "批次质量异常：该品下场停播，质量整改通过前不得排播")
		}
		if a.batchN == 0 && a.unqN == 0 {
			if a.promiseN > 0 {
				factor = mathMin(factor, 0.7)
				reasons = append(reasons, fmt.Sprintf("主播承诺不符 %d 单：口播整改前压减备货至 70%%", a.promiseN))
			}
			if a.logiN > 0 {
				factor = mathMin(factor, 0.8)
				reasons = append(reasons, fmt.Sprintf("物流延迟 %d 单：压减至 80%% 并收缩远区可售范围", a.logiN))
			}
			if a.tasteN > 0 {
				factor = mathMin(factor, 0.9)
				reasons = append(reasons, fmt.Sprintf("口味问题 %d 单：压减至 90%%，同步调整配方与详情标注", a.tasteN))
			}
		}
		suggest := round2(base * factor)
		if len(reasons) == 0 {
			reasons = append(reasons, "无显著风险维度，按本场净销量维持备货")
		}
		reasonText := stringsJoin(reasons, "；")

		if _, err := tx.Exec(ctx, `
			INSERT INTO live_restock_advice
			  (live_return_id,finished_batch_id,product_name,shipped_kg,returned_kg,return_rate,suggest_qty_kg,reason)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			liveReturnID, a.finID, a.product, round2(shippedKg), round2(a.returnedKg),
			rate, suggest, reasonText); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"product_name": a.product, "shipped_kg": round2(shippedKg),
			"returned_kg": round2(a.returnedKg), "return_rate": rate,
			"suggest_qty_kg": suggest, "reason": reasonText,
		})
	}
	return out, nil
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func stringsJoin(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func na(s string) string {
	if s == "" {
		return "未留档"
	}
	return s
}
