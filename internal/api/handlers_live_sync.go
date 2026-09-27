package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// syncLiveReview 将复盘结论（三项对比 + 建议 + 下场备货调整）同步给直播渠道，
// 渠道侧据此落实扣款、整改与排播备货；同步后批次进入 synced。
func (s *Server) syncLiveReview(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ctx := r.Context()

	var status string
	var chID *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT status,channel_id FROM live_return_batches WHERE id=$1`, id).Scan(&status, &chID); err != nil {
		writeErr(w, 404, "直播退货批次不存在")
		return
	}
	var rv promise
	_ = s.pool.QueryRow(ctx, `SELECT promise_compare,logistics_compare,quality_compare,summary
		FROM live_return_reviews WHERE live_return_id=$1`, id).
		Scan(&rv.p, &rv.l, &rv.q, &rv.s)
	if rv.s == "" {
		writeErr(w, 409, "请先完成复盘，再同步渠道")
		return
	}

	sugRows, _ := s.pool.Query(ctx, `
		SELECT stype,title,content,amount,status FROM live_review_suggestions
		WHERE live_return_id=$1 ORDER BY id`, id)
	sugs := scanMaps(sugRows)
	sugRows.Close()
	rsRows, _ := s.pool.Query(ctx, `
		SELECT product_name,shipped_kg,returned_kg,return_rate,suggest_qty_kg,reason
		FROM live_restock_advice WHERE live_return_id=$1 ORDER BY id`, id)
	restocks := scanMaps(rsRows)
	rsRows.Close()

	var b strings.Builder
	b.WriteString("【直播集中退货渠道复盘同步单】\n")
	b.WriteString(rv.s + "\n\n")
	b.WriteString("一、处置建议：\n")
	typeName := map[string]string{
		"channel_deduction":        "渠道扣款",
		"quality_rectification":    "质量整改",
		"logistics_accountability": "物流追责",
	}
	var deductTotal float64
	for _, sg := range sugs {
		st, _ := sg["stype"].(string)
		b.WriteString(fmt.Sprintf("- [%s] %v：%v", typeName[st], sg["title"], sg["content"]))
		if amt, ok := sg["amount"].(float64); ok && amt > 0 {
			b.WriteString(fmt.Sprintf("（涉及 %.2f 元）", amt))
			if st == "channel_deduction" {
				deductTotal += amt
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n二、下一场直播备货调整（复盘结果直接影响排播备货）：\n")
	for _, x := range restocks {
		b.WriteString(fmt.Sprintf("- %v：本场发货 %vkg / 退回 %vkg（退货率 %v%%），下场建议备货 %vkg。%v\n",
			x["product_name"], x["shipped_kg"], x["returned_kg"], x["return_rate"],
			x["suggest_qty_kg"], x["reason"]))
	}
	b.WriteString("\n请渠道在 3 个工作日内确认主播口播整改、扣款对账金额与下场排播备货量。")

	syncNo := docNo("CRS")
	var syncID int64
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `
		INSERT INTO channel_review_sync(sync_no,live_return_id,channel_id,content,status,synced_at)
		VALUES($1,$2,$3,$4,'synced',now()) RETURNING id`,
		syncNo, id, chID, b.String()).Scan(&syncID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE live_return_batches SET status='synced' WHERE id=$1 AND status='reviewed'`, id)
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"sync_id": syncID, "sync_no": syncNo, "status": "synced",
		"channel_deduction_total": round2(deductTotal),
		"restock_products":        len(restocks),
	})
}

type promise struct{ p, l, q, s string }

// respondSync 渠道回执确认
func (s *Server) respondSync(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	syncID, _ := strconv.ParseInt(chi.URLParam(r, "syncId"), 10, 64)
	var req struct {
		Response string `json:"response"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "字段错误")
		return
	}
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE channel_review_sync SET status='acknowledged',response=$3
		 WHERE id=$1 AND live_return_id=$2`,
		syncID, id, req.Response)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 404, "同步单不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"sync_id": syncID, "status": "acknowledged"})
}

// closeLiveReturn 结案
func (s *Server) closeLiveReturn(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE live_return_batches SET status='closed',closed_at=now() WHERE id=$1 AND status IN ('synced','reviewed')`, id)
	if err != nil || tag.RowsAffected() == 0 {
		writeErr(w, 409, "批次不存在或尚未完成复盘/同步，无法结案")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": "closed"})
}
