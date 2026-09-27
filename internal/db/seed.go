package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Seed 幂等写入演示数据（全部 ON CONFLICT DO NOTHING）。
func Seed(ctx context.Context, p *pgxpool.Pool) error {
	users := []struct{ user, pass, name, role string }{
		{"admin", "admin123", "系统管理员", "admin"},
		{"manager", "manager123", "工厂经理", "manager"},
		{"quality", "quality123", "品控主管", "quality"},
		{"warehouse", "ware123", "冷库仓管", "warehouse"},
		{"production", "prod123", "产线班长", "production"},
		{"finance", "finance123", "财务专员", "finance"},
	}
	for _, u := range users {
		h, err := bcrypt.GenerateFromPassword([]byte(u.pass), bcrypt.MinCost)
		if err != nil {
			return err
		}
		if _, err := p.Exec(ctx, `
			INSERT INTO users(username,password_hash,full_name,role)
			VALUES($1,$2,$3,$4) ON CONFLICT (username) DO NOTHING`,
			u.user, string(h), u.name, u.role); err != nil {
			return err
		}
	}

	stmts := []string{
		`INSERT INTO suppliers(code,name,contact) VALUES
		 ('S001','荣成远洋水产有限公司','王经理 13800000001'),
		 ('S002','大连獐子岛渔业合作社','李经理 13800000002'),
		 ('S003','舟山鱿鱼捕捞合作社','张经理 13800000003'),
		 ('S004','北海鱼柳加工厂','赵经理 13800000004')
		 ON CONFLICT (code) DO NOTHING`,

		`INSERT INTO production_lines(code,name) VALUES
		 ('L1','一号解冻加工线'),('L2','二号解冻加工线')
		 ON CONFLICT (code) DO NOTHING`,

		`INSERT INTO seasoning_batches(batch_no,name,supplier,expiry_date) VALUES
		 ('T001','蒜香腌料','味之源食品','2027-01-01'),
		 ('T002','麻辣腌料','味之源食品','2027-03-01')
		 ON CONFLICT (batch_no) DO NOTHING`,

		// 4 个原料批次：RM03 入库温度 -15.1 超过冻品 ≤-18℃ 限值
		`INSERT INTO raw_material_batches
		 (batch_no,supplier_id,species,catch_or_production_date,inbound_temp_c,quarantine_cert,box_no,location,qty_kg,status,created_by)
		 SELECT 'RM20260901',id,'虾仁','2026-08-28',-18.20,'QY2026-0001','BX-R-001','冷库A-01',500,'in_storage',1 FROM suppliers WHERE code='S001'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO raw_material_batches
		 (batch_no,supplier_id,species,catch_or_production_date,inbound_temp_c,quarantine_cert,box_no,location,qty_kg,status,created_by)
		 SELECT 'RM20260902',id,'扇贝','2026-08-29',-17.80,'QY2026-0002','BX-S-001','冷库A-02',300,'in_storage',1 FROM suppliers WHERE code='S002'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO raw_material_batches
		 (batch_no,supplier_id,species,catch_or_production_date,inbound_temp_c,quarantine_cert,box_no,location,qty_kg,status,created_by)
		 SELECT 'RM20260903',id,'鱿鱼','2026-08-30',-15.10,'QY2026-0003','BX-U-001','冷库A-03',400,'locked',1 FROM suppliers WHERE code='S003'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO raw_material_batches
		 (batch_no,supplier_id,species,catch_or_production_date,inbound_temp_c,quarantine_cert,box_no,location,qty_kg,status,created_by)
		 SELECT 'RM20260904',id,'鱼柳','2026-08-31',-18.50,'QY2026-0004','BX-F-001','冷库B-01',600,'in_storage',1 FROM suppliers WHERE code='S004'
		 ON CONFLICT (batch_no) DO NOTHING`,

		`INSERT INTO temp_logs(raw_batch_id,temp_c,operator,note,alarm)
		 SELECT id,-15.10,'仓管孙七','入库测温高于 -18℃ 限值，锁定待判',true
		 FROM raw_material_batches WHERE batch_no='RM20260903'
		   AND NOT EXISTS(SELECT 1 FROM temp_logs tl WHERE tl.raw_batch_id=raw_material_batches.id AND tl.alarm)`,

		// 生产计划（含渠道定制包装要求）
		`INSERT INTO production_plans(plan_no,product_name,line_id,target_qty_kg,packaging_spec,required_seasoning_batch,status,created_by)
		 SELECT 'PP20260910-01','蒜香虾仁',id,240,'直播间定制 250g 彩包','T001','finished',1 FROM production_lines WHERE code='L1'
		 ON CONFLICT (plan_no) DO NOTHING`,
		`INSERT INTO production_plans(plan_no,product_name,line_id,target_qty_kg,packaging_spec,required_seasoning_batch,status,created_by)
		 SELECT 'PP20260910-02','麻辣鱿鱼花',id,180,'商超定制 500g 简包','T002','finished',1 FROM production_lines WHERE code='L2'
		 ON CONFLICT (plan_no) DO NOTHING`,

		// 解冻记录
		`INSERT INTO thaw_records(plan_id,raw_batch_id,start_at,end_at,center_temp_c,water_batch_no,operator)
		 SELECT p.id,r.id,'2026-09-10 06:00+08','2026-09-10 09:00+08',2.10,'W-260910-01','刘解冻'
		 FROM production_plans p, raw_material_batches r
		 WHERE p.plan_no='PP20260910-01' AND r.batch_no='RM20260901'
		   AND NOT EXISTS(SELECT 1 FROM thaw_records t WHERE t.plan_id=p.id)`,
		`INSERT INTO thaw_records(plan_id,raw_batch_id,start_at,end_at,center_temp_c,water_batch_no,operator)
		 SELECT p.id,r.id,'2026-09-10 06:30+08','2026-09-10 11:20+08',3.40,'W-260910-02','刘解冻'
		 FROM production_plans p, raw_material_batches r
		 WHERE p.plan_no='PP20260910-02' AND r.batch_no='RM20260903'
		   AND NOT EXISTS(SELECT 1 FROM thaw_records t WHERE t.plan_id=p.id)`,

		// 加工七步记录（wash/trim/season/weigh/package/metalcheck/coldstorage）
		`INSERT INTO process_records(plan_id,stage,operator,value_num,value_text,ok,note)
		 SELECT p.id,s.st,'周加工',s.vn,s.vt,s.ok,s.nt FROM production_plans p
		 CROSS JOIN (VALUES
		   ('wash','清水三道漂洗',NULL::numeric,'电导率 820us/cm',true,''),
		   ('trim','去线分切',NULL,'规格 3g/只',true,''),
		   ('season','按 T001 投料',NULL,'T001',true,'蒜香腌料 12kg'),
		   ('weigh','称重',0.25,'250g/盒',true,''),
		   ('package','直播彩包充氮',NULL,'250g 彩包',true,''),
		   ('metalcheck','金检 Fe1.5/NonFe2.0/Sus2.5',NULL,'通过',true,''),
		   ('coldstorage','入成品冷库',-18.0,'冷库C-01',true,'')
		 ) AS s(st,nt,vn,vt,ok,ign)
		 WHERE p.plan_no='PP20260910-01'
		   AND NOT EXISTS(SELECT 1 FROM process_records pr WHERE pr.plan_id=p.id)`,
		`INSERT INTO process_records(plan_id,stage,operator,value_num,value_text,ok,note)
		 SELECT p.id,s.st,'马加工',s.vn,s.vt,s.ok,s.nt FROM production_plans p
		 CROSS JOIN (VALUES
		   ('wash','清水三道漂洗',NULL::numeric,'电导率 910us/cm',true,''),
		   ('trim','切花刀',NULL,'4cm 花刀',true,''),
		   ('season','按 T002 投料',NULL,'T002',true,'麻辣腌料 9kg'),
		   ('weigh','称重',0.50,'500g/盒',true,''),
		   ('package','商超简包',NULL,'500g 简包',true,''),
		   ('metalcheck','金检 Fe1.5/NonFe2.0/Sus2.5',NULL,'通过',true,''),
		   ('coldstorage','入成品冷库',-18.0,'冷库C-02',true,'')
		 ) AS s(st,nt,vn,vt,ok,ign)
		 WHERE p.plan_no='PP20260910-02'
		   AND NOT EXISTS(SELECT 1 FROM process_records pr WHERE pr.plan_id=p.id)`,

		// 成品批次
		`INSERT INTO finished_batches(batch_no,plan_id,product_name,packaging_spec,qty_kg,cold_storage_location,created_by)
		 SELECT 'FG20260912-01',id,'蒜香虾仁','250g 直播彩包',240,'冷库C-01',1 FROM production_plans WHERE plan_no='PP20260910-01'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO finished_batches(batch_no,plan_id,product_name,packaging_spec,qty_kg,cold_storage_location,created_by)
		 SELECT 'FG20260912-02',id,'麻辣鱿鱼花','500g 商超简包',180,'冷库C-02',1 FROM production_plans WHERE plan_no='PP20260910-02'
		 ON CONFLICT (batch_no) DO NOTHING`,

		// 原料-成品关联
		`INSERT INTO batch_links(finished_batch_id,raw_batch_id,qty_kg)
		 SELECT f.id,r.id,240 FROM finished_batches f, raw_material_batches r
		 WHERE f.batch_no='FG20260912-01' AND r.batch_no='RM20260901'
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO batch_links(finished_batch_id,raw_batch_id,qty_kg)
		 SELECT f.id,r.id,180 FROM finished_batches f, raw_material_batches r
		 WHERE f.batch_no='FG20260912-02' AND r.batch_no='RM20260903'
		 ON CONFLICT DO NOTHING`,

		// 留样
		`INSERT INTO samples(sample_no,finished_batch_id,result,tested_at,lab_note,inspector)
		 SELECT 'SP-001',id,'qualified','2026-09-12 16:00+08','微生物/感官均合格','品控钱九'
		 FROM finished_batches WHERE batch_no='FG20260912-01'
		 ON CONFLICT (sample_no) DO NOTHING`,
		`INSERT INTO samples(sample_no,finished_batch_id,result,tested_at,lab_note,inspector)
		 SELECT 'SP-002',id,'qualified','2026-09-12 16:10+08','初检合格','品控钱九'
		 FROM finished_batches WHERE batch_no='FG20260912-02'
		 ON CONFLICT (sample_no) DO NOTHING`,

		// 渠道
		`INSERT INTO channels(code,name,ctype) VALUES
		 ('CH01','盒马鲜生(华东)','商超'),('CH02','天猫旗舰店','电商'),
		 ('CH03','抖音海鲜直播间','直播'),('CH04','华东餐饮经销商','经销')
		 ON CONFLICT (code) DO NOTHING`,

		// 发货
		`INSERT INTO shipments(ship_no,channel_id,status)
		 SELECT 'SH20260913-01',id,'shipped' FROM channels WHERE code='CH03'
		 ON CONFLICT (ship_no) DO NOTHING`,
		`INSERT INTO shipments(ship_no,channel_id,status)
		 SELECT 'SH20260913-02',id,'shipped' FROM channels WHERE code='CH01'
		 ON CONFLICT (ship_no) DO NOTHING`,
		`INSERT INTO shipment_items(shipment_id,finished_batch_id,boxes,qty_kg)
		 SELECT s.id,f.id,200,50 FROM shipments s, finished_batches f
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-01'
		   AND NOT EXISTS(SELECT 1 FROM shipment_items si WHERE si.shipment_id=s.id)`,
		`INSERT INTO shipment_items(shipment_id,finished_batch_id,boxes,qty_kg)
		 SELECT s.id,f.id,160,80 FROM shipments s, finished_batches f
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-02'
		   AND NOT EXISTS(SELECT 1 FROM shipment_items si WHERE si.shipment_id=s.id AND si.finished_batch_id=f.id)`,
		`INSERT INTO shipment_items(shipment_id,finished_batch_id,boxes,qty_kg)
		 SELECT s.id,f.id,300,75 FROM shipments s, finished_batches f
		 WHERE s.ship_no='SH20260913-02' AND f.batch_no='FG20260912-01'
		   AND NOT EXISTS(SELECT 1 FROM shipment_items si WHERE si.shipment_id=s.id)`,

		// 已售订单（精确到箱）
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260914-001',s.id,f.id,c.id,'BX-LIVE-0001',2,'直播观众周**','sold'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-02' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260914-002',s.id,f.id,c.id,'BX-LIVE-0002',1,'直播观众吴**','sold'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-02' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260914-003',s.id,f.id,c.id,'BX-LIVE-0003',3,'直播观众郑**','sold'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-01' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260915-010',s.id,f.id,c.id,'BX-HM-0010',10,'盒马张江店','sold'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-02' AND f.batch_no='FG20260912-01' AND c.code='CH01'
		 ON CONFLICT (order_no) DO NOTHING`,

		// 渠道反馈：直播间反馈鱿鱼花异味
		`INSERT INTO channel_feedback(channel_id,order_id,finished_batch_id,ftype,content,handled)
		 SELECT c.id,o.id,f.id,'odor','顾客反馈解冻后有氨味，直播间集中差评',false
		 FROM channels c, orders o, finished_batches f
		 WHERE c.code='CH03' AND o.order_no='OD20260914-001' AND f.batch_no='FG20260912-02'
		   AND NOT EXISTS(SELECT 1 FROM channel_feedback WHERE ftype='odor')`,

		// 冷库库存（剩余库存）
		`INSERT INTO inventory(finished_batch_id,location,qty_kg,status)
		 SELECT id,'冷库C-01',115,'available' FROM finished_batches WHERE batch_no='FG20260912-01'
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO inventory(finished_batch_id,location,qty_kg,status)
		 SELECT id,'冷库C-02',100,'available' FROM finished_batches WHERE batch_no='FG20260912-02'
		 ON CONFLICT DO NOTHING`,

		// 一条未结案的品控事件：鱿鱼原料入库温度超标（尚未召回，供演示处置）
		`INSERT INTO quality_events(event_no,etype,severity,raw_batch_id,description,status,created_by)
		 SELECT 'EV20260903-01','temp_exceed','major',id,'RM20260903 入库温度 -15.1℃，高于冻品限值 -18℃','open',3
		 FROM raw_material_batches WHERE batch_no='RM20260903'
		 ON CONFLICT (event_no) DO NOTHING`,

		// ===== 直播间集中退货演示（已登记、待复盘）=====
		// 4 个直播渠道订单，分别对应四类退货原因
		`INSERT INTO orders(order_no,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260916-L01',f.id,c.id,'BX-LR-0001',2,'直播观众冯**','returned'
		 FROM finished_batches f, channels c
		 WHERE f.batch_no='FG20260912-02' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260916-L02',f.id,c.id,'BX-LR-0002',1,'直播观众陈**','returned'
		 FROM finished_batches f, channels c
		 WHERE f.batch_no='FG20260912-01' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260916-L03',f.id,c.id,'BX-LR-0003',1,'直播观众褚**','returned'
		 FROM finished_batches f, channels c
		 WHERE f.batch_no='FG20260912-01' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,finished_batch_id,channel_id,box_no,qty,customer,status)
		 SELECT 'OD20260916-L04',f.id,c.id,'BX-LR-0004',1,'直播观众卫**','returned'
		 FROM finished_batches f, channels c
		 WHERE f.batch_no='FG20260912-02' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,

		// 集中退货批次主单（留存主播承诺快照）
		`INSERT INTO live_return_batches
		 (return_no,live_session,channel_id,anchor_name,finished_batch_id,
		  promised_arrival_hours,promised_quality,promised_shelf_life,status,created_by,note)
		 SELECT 'LR20260916-01','抖音海鲜专场 LS20260916',c.id,'海鲜一姐',f.id,
		        48,'现剥大颗虾仁/肥厚鱿鱼花，冷链直达、坏单包赔','冷藏保鲜 7 天','registered',3,
		        '9·16 专场后 48 小时内集中退货，四类原因均有'
		 FROM channels c, finished_batches f
		 WHERE c.code='CH03' AND f.batch_no='FG20260912-02'
		 ON CONFLICT (return_no) DO NOTHING`,

		// 退货明细：批次质量/承诺不符/物流延迟/口味问题
		`INSERT INTO live_return_items
		 (live_return_id,order_id,finished_batch_id,box_no,qty,reason,
		  promised_arrival_hours,actual_arrival_hours,quality_result,evidence)
		 SELECT b.id,o.id,o.finished_batch_id,v.box,v.qty,v.reason,
		        v.promised,v.actual,NULLIF(v.quality,''),v.evidence
		 FROM (VALUES
		   ('OD20260916-L01','BX-LR-0001',2,'batch_quality',48,56,'unqualified','顾客照片：包装鼓胀、解冻后有氨味'),
		   ('OD20260916-L02','BX-LR-0002',1,'promise_mismatch',48,40,'','实物虾仁偏小且化冻缩水，与口播大颗虾仁不符'),
		   ('OD20260916-L03','BX-LR-0003',1,'logistics_delay',48,76,'','物流 76 小时才到，冰袋已化'),
		   ('OD20260916-L04','BX-LR-0004',1,'taste_issue',48,44,'','调味过咸、腥味重')
		 ) AS v(order_no,box,qty,reason,promised,actual,quality,evidence)
		 JOIN orders o ON o.order_no=v.order_no
		 JOIN live_return_batches b ON b.return_no='LR20260916-01'
		 WHERE NOT EXISTS (
		   SELECT 1 FROM live_return_items i WHERE i.live_return_id=b.id AND i.box_no=v.box)`,

		// ① 退款单（59.9 元/盒）
		`INSERT INTO live_refunds(refund_no,live_return_id,item_id,order_id,amount,reason)
		 SELECT 'LRF-DEMO-'||i.id,b.id,i.id,i.order_id,ROUND((i.qty*59.9)::numeric,2),
		        '直播集中退货-'||CASE i.reason
		          WHEN 'promise_mismatch' THEN '主播承诺不符'
		          WHEN 'logistics_delay' THEN '物流延迟'
		          WHEN 'taste_issue' THEN '口味问题'
		          WHEN 'batch_quality' THEN '批次质量异常' END
		 FROM live_return_items i JOIN live_return_batches b ON b.id=i.live_return_id
		 WHERE b.return_no='LR20260916-01'
		   AND NOT EXISTS (SELECT 1 FROM live_refunds r WHERE r.item_id=i.id)`,

		// 实物退货回厂登记（衔接三态判定流程）
		`INSERT INTO return_goods(return_no,finished_batch_id,box_no,qty_kg,note)
		 SELECT 'RT-LRDEMO-'||ROW_NUMBER() OVER (ORDER BY i.id),i.finished_batch_id,i.box_no,
		        CASE i.box_no WHEN 'BX-LR-0001' THEN 1.0 WHEN 'BX-LR-0002' THEN 0.25
		                      WHEN 'BX-LR-0003' THEN 0.25 WHEN 'BX-LR-0004' THEN 0.5 END,
		        '直播集中退货 LR20260916-01（'||CASE i.reason
		          WHEN 'promise_mismatch' THEN '主播承诺不符'
		          WHEN 'logistics_delay' THEN '物流延迟'
		          WHEN 'taste_issue' THEN '口味问题'
		          WHEN 'batch_quality' THEN '批次质量异常' END||'），待回厂品控判定'
		 FROM live_return_items i
		 JOIN live_return_batches b ON b.id=i.live_return_id
		 WHERE b.return_no='LR20260916-01'
		   AND NOT EXISTS (SELECT 1 FROM return_goods rg WHERE rg.note LIKE '直播集中退货 LR20260916-01%' AND rg.box_no=i.box_no)`,

		// ② 库存退回单（按原因建议处置）
		`INSERT INTO live_stock_returns
		 (stock_no,live_return_id,item_id,return_goods_id,finished_batch_id,box_no,qty_kg,
		  suggested_disposition,target_location,note)
		 SELECT 'LRS-DEMO-'||i.id,b.id,i.id,rg.id,i.finished_batch_id,i.box_no,rg.qty_kg,
		        CASE i.reason WHEN 'taste_issue' THEN 'rework'
		                      WHEN 'batch_quality' THEN 'destroy'
		                      ELSE 'reinspect' END,
		        CASE i.reason WHEN 'taste_issue' THEN '返工区'
		                      WHEN 'batch_quality' THEN '销毁区'
		                      ELSE '复检待判区' END,
		        CASE i.reason
		          WHEN 'promise_mismatch' THEN '商品本身无质量投诉，复检合格后可再售'
		          WHEN 'logistics_delay' THEN '时效违约，温度记录/感官待复检确认'
		          WHEN 'taste_issue' THEN '口味偏差可重新调味返工'
		          WHEN 'batch_quality' THEN '批次质量异常，禁止再售/返工，必须销毁' END
		 FROM live_return_items i
		 JOIN live_return_batches b ON b.id=i.live_return_id
		 JOIN return_goods rg ON rg.box_no=i.box_no
		   AND rg.finished_batch_id=i.finished_batch_id AND rg.note LIKE '直播集中退货 LR20260916-01%'
		 WHERE b.return_no='LR20260916-01'
		   AND NOT EXISTS (SELECT 1 FROM live_stock_returns s WHERE s.item_id=i.id)`,

		// ③ 客服话术（按原因分别生成）
		`INSERT INTO live_cs_scripts(script_no,live_return_id,item_id,reason,audience,title,content)
		 SELECT 'LCS-DEMO-'||i.id,b.id,i.id,i.reason,'直播间下单顾客',
		        CASE i.reason
		          WHEN 'promise_mismatch' THEN '主播承诺不符 · 致歉与全额退款话术'
		          WHEN 'logistics_delay' THEN '物流延迟 · 致歉与运费补偿话术'
		          WHEN 'taste_issue' THEN '口味问题 · 解释与换货/返工话术'
		          WHEN 'batch_quality' THEN '批次质量异常 · 紧急停售与召回告知话术' END,
		        CASE i.reason
		          WHEN 'promise_mismatch' THEN '您好，经核对直播口播与实物，确有承诺与实物不符，郑重致歉；已发起全额退款（59.9 元/盒，1-3 工作日原路退回），商品冷链取回复检，不再作原单二次销售。'
		          WHEN 'logistics_delay' THEN '您好，本场承诺 48 小时送达，您实际 76 小时收到，已超时效；办理全额退款并调取该路由冷链温度记录追责承运方，异常商品请勿食用。'
		          WHEN 'taste_issue' THEN '您好，口味未达预期深表歉意；本单退款（亦可更换口味），退回商品转返工区重新调味评估，下场直播将调整配方并客观标注风味。'
		          WHEN 'batch_quality' THEN '您好，研判为批次质量风险：该批次立即停售、在库隔离并登记品控事件评估召回；请暂停食用、保留包装拍照，全额退款并按召回政策赔付。' END
		 FROM live_return_items i JOIN live_return_batches b ON b.id=i.live_return_id
		 WHERE b.return_no='LR20260916-01'
		   AND NOT EXISTS (SELECT 1 FROM live_cs_scripts c WHERE c.item_id=i.id)`,

		// ④ 渠道扣款单（按原因适用扣款类型与比例）
		`INSERT INTO live_channel_charges
		 (charge_no,live_return_id,item_id,channel_id,amount,charge_type,reason,status)
		 SELECT 'LCC-DEMO-'||i.id,b.id,i.id,b.channel_id,
		        ROUND((i.qty*59.9*CASE i.reason
		          WHEN 'promise_mismatch' THEN 1.0
		          WHEN 'logistics_delay' THEN 0.8
		          WHEN 'taste_issue' THEN 0.2
		          WHEN 'batch_quality' THEN 0.5 END)::numeric,2),
		        CASE i.reason
		          WHEN 'promise_mismatch' THEN 'commitment_mismatch'
		          WHEN 'logistics_delay' THEN 'logistics_breach'
		          WHEN 'taste_issue' THEN 'service_share'
		          WHEN 'batch_quality' THEN 'quality_penalty' END,
		        '9·16 直播专场集中退货对账（'||CASE i.reason
		          WHEN 'promise_mismatch' THEN '主播承诺不符，全额承担'
		          WHEN 'logistics_delay' THEN '物流时效违约，80%'
		          WHEN 'taste_issue' THEN '客诉服务分担，20%'
		          WHEN 'batch_quality' THEN '批次质量违约金，50%' END||'）','pending'
		 FROM live_return_items i JOIN live_return_batches b ON b.id=i.live_return_id
		 WHERE b.return_no='LR20260916-01'
		   AND NOT EXISTS (SELECT 1 FROM live_channel_charges c WHERE c.item_id=i.id)`,
	}
	for _, s := range stmts {
		if _, err := p.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
