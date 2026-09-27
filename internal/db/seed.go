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

		// ================= 直播间集中退货 演示数据 =================

		// 直播场次：LS...01 退货进行中；LS...02 已复盘结案
		`INSERT INTO live_sessions(session_no,channel_id,anchor,title,live_at,promised_delivery_hours,promise_text,status,created_by)
		 SELECT 'LS20260914-01',id,'海味小厨','开海节直播专场','2026-09-14 19:00+08',48,
		        '全场顺丰冷链 48 小时送达；坏单包赔；缺斤少两退一赔三','returning',1
		 FROM channels WHERE code='CH03' ON CONFLICT (session_no) DO NOTHING`,
		`INSERT INTO live_sessions(session_no,channel_id,anchor,title,live_at,promised_delivery_hours,promise_text,status,created_by)
		 SELECT 'LS20260916-02',id,'海味小厨','开海节返场直播','2026-09-16 19:00+08',48,
		        '全场顺丰冷链 48 小时送达；坏单包赔；缺斤少两退一赔三','reviewed',1
		 FROM channels WHERE code='CH03' ON CONFLICT (session_no) DO NOTHING`,

		// 返场直播订单（供承诺不符/物流延迟退货关联）
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status,sold_at)
		 SELECT 'OD20260916-005',s.id,f.id,c.id,'BX-LIVE-0005',2,'直播观众王**','sold','2026-09-16 20:10+08'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-01' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,
		`INSERT INTO orders(order_no,shipment_id,finished_batch_id,channel_id,box_no,qty,customer,status,sold_at)
		 SELECT 'OD20260916-006',s.id,f.id,c.id,'BX-LIVE-0006',1,'直播观众林**','sold','2026-09-16 20:25+08'
		 FROM shipments s, finished_batches f, channels c
		 WHERE s.ship_no='SH20260913-01' AND f.batch_no='FG20260912-01' AND c.code='CH03'
		 ON CONFLICT (order_no) DO NOTHING`,

		// 集中退货批次①（进行中）：麻辣鱿鱼花 批次质量异常 + 口味问题
		`INSERT INTO live_return_batches(batch_no,session_id,finished_batch_id,opened_by,note)
		 SELECT 'LRB20260915-01',ls.id,f.id,3,'直播后观众集中反馈鱿鱼花异味，开启集中退货'
		 FROM live_sessions ls, finished_batches f
		 WHERE ls.session_no='LS20260914-01' AND f.batch_no='FG20260912-02'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO live_return_orders(entry_no,batch_id,order_id,reason_cat,reason_detail,qty,qty_kg,amount,
		   promised_delivery_hours,actual_delivery_hours,sample_result,registered_by)
		 SELECT 'LE20260915-001',b.id,o.id,'batch_quality','解冻后有氨味，疑似批次变质',1,0.50,69.90,
		        48,50.0,'送检不合格（挥发性盐基氮超标）',3
		 FROM live_return_batches b, orders o
		 WHERE b.batch_no='LRB20260915-01' AND o.order_no='OD20260914-001'
		 ON CONFLICT (entry_no) DO NOTHING`,
		`INSERT INTO live_return_orders(entry_no,batch_id,order_id,reason_cat,reason_detail,qty,qty_kg,amount,
		   promised_delivery_hours,actual_delivery_hours,sample_result,registered_by)
		 SELECT 'LE20260915-002',b.id,o.id,'taste_problem','顾客反馈麻辣味发苦、口感发柴',1,0.50,69.90,
		        48,44.0,'合格',3
		 FROM live_return_batches b, orders o
		 WHERE b.batch_no='LRB20260915-01' AND o.order_no='OD20260914-002'
		 ON CONFLICT (entry_no) DO NOTHING`,
		// 批次①四项处置（待执行，供演示逐项执行）
		`INSERT INTO live_return_disposals(doc_no,entry_id,dtype,title,content,amount)
		 SELECT d.doc,e.id,d.dt,d.ti,d.ct,d.am FROM live_return_orders e
		 CROSS JOIN (VALUES
		   ('LD20260915-001-R','refund','退款-LE20260915-001','向消费者退款 69.90 元（1 盒 × 69.90 元/盒），订单 OD20260914-001，原因：批次质量异常。',69.90::numeric),
		   ('LD20260915-001-S','restock','库存退回-LE20260915-001','退回商品 1 盒 / 0.50 kg（成品 FG20260912-02 麻辣鱿鱼花）登记回厂，入「直播退货仓」隔离待判，后续按可复检/可返工/必须销毁三态处置。',0.50),
		   ('LD20260915-001-C','script','客服话术-LE20260915-001','您好，经核实您反馈的商品存在批次质量异常。我们已第一时间为您全额退款 69.90 元，并对该批次启动复检与召回评估，请您不要继续食用。给您带来担忧，我们深表歉意。',0.00),
		   ('LD20260915-001-D','channel_deduction','渠道扣款-LE20260915-001','非渠道责任（物流/工厂质量），渠道扣款 0 元：0.00 元（退款基数 69.90 元）。',0.00)
		 ) AS d(doc,dt,ti,ct,am)
		 WHERE e.entry_no='LE20260915-001' ON CONFLICT (doc_no) DO NOTHING`,
		`INSERT INTO live_return_disposals(doc_no,entry_id,dtype,title,content,amount)
		 SELECT d.doc,e.id,d.dt,d.ti,d.ct,d.am FROM live_return_orders e
		 CROSS JOIN (VALUES
		   ('LD20260915-002-R','refund','退款-LE20260915-002','向消费者退款 69.90 元（1 盒 × 69.90 元/盒），订单 OD20260914-002，原因：口味问题。',69.90::numeric),
		   ('LD20260915-002-S','restock','库存退回-LE20260915-002','退回商品 1 盒 / 0.50 kg（成品 FG20260912-02 麻辣鱿鱼花）登记回厂，入「直播退货仓」隔离待判，后续按可复检/可返工/必须销毁三态处置。',0.50),
		   ('LD20260915-002-C','script','客服话术-LE20260915-002','您好，很抱歉商品口味未达预期（顾客反馈麻辣味发苦、口感发柴）。我们已为您办理退款 69.90 元，并将您的反馈同步给调味工艺组整改；欢迎下次直播再给我们一次机会。',0.00),
		   ('LD20260915-002-D','channel_deduction','渠道扣款-LE20260915-002','口味问题渠道选品连带，按退款额 30% 扣款：20.97 元（退款基数 69.90 元）。',20.97)
		 ) AS d(doc,dt,ti,ct,am)
		 WHERE e.entry_no='LE20260915-002' ON CONFLICT (doc_no) DO NOTHING`,

		// 集中退货批次②（已结案）：蒜香虾仁 主播承诺不符 + 物流延迟
		`INSERT INTO live_return_batches(batch_no,session_id,finished_batch_id,opened_by,opened_at,closed_at,status,note)
		 SELECT 'LRB20260917-01',ls.id,f.id,3,'2026-09-17 09:00+08','2026-09-17 18:00+08','closed',
		        '返场直播承诺不符与物流延迟集中退货'
		 FROM live_sessions ls, finished_batches f
		 WHERE ls.session_no='LS20260916-02' AND f.batch_no='FG20260912-01'
		 ON CONFLICT (batch_no) DO NOTHING`,
		`INSERT INTO live_return_orders(entry_no,batch_id,order_id,reason_cat,reason_detail,qty,qty_kg,amount,
		   promised_delivery_hours,actual_delivery_hours,sample_result,registered_by)
		 SELECT 'LE20260917-001',b.id,o.id,'promise_mismatch','主播承诺买二送一赠品未随箱',2,0.50,119.80,
		        48,45.0,'合格',3
		 FROM live_return_batches b, orders o
		 WHERE b.batch_no='LRB20260917-01' AND o.order_no='OD20260916-005'
		 ON CONFLICT (entry_no) DO NOTHING`,
		`INSERT INTO live_return_orders(entry_no,batch_id,order_id,reason_cat,reason_detail,qty,qty_kg,amount,
		   promised_delivery_hours,actual_delivery_hours,sample_result,registered_by)
		 SELECT 'LE20260917-002',b.id,o.id,'logistics_delay','冷链延迟 60 小时送达，冰袋已化',1,0.25,59.90,
		        48,60.0,'合格',3
		 FROM live_return_batches b, orders o
		 WHERE b.batch_no='LRB20260917-01' AND o.order_no='OD20260916-006'
		 ON CONFLICT (entry_no) DO NOTHING`,
		// 批次②四项处置（均已执行）
		`INSERT INTO live_return_disposals(doc_no,entry_id,dtype,title,content,amount,status,executed_by,executed_at)
		 SELECT d.doc,e.id,d.dt,d.ti,d.ct,d.am,'done',d.ex,'2026-09-17 17:00+08' FROM live_return_orders e
		 CROSS JOIN (VALUES
		   ('LD20260917-001-R','refund','退款-LE20260917-001','向消费者退款 119.80 元（2 盒 × 59.90 元/盒），订单 OD20260916-005，原因：主播承诺不符。',119.80::numeric,'finance'),
		   ('LD20260917-001-S','restock','库存退回-LE20260917-001','退回商品 2 盒 / 0.50 kg（成品 FG20260912-01 蒜香虾仁）登记回厂，入「直播退货仓」隔离待判。',0.50,'warehouse'),
		   ('LD20260917-001-C','script','客服话术-LE20260917-001','您好，非常抱歉本次直播商品与主播承诺不符（主播承诺买二送一赠品未随箱）。我们已为您办理全额退款 119.80 元，无需寄回商品；后续直播承诺文案将经品控审核后再上架，感谢您的监督。',0.00,'quality'),
		   ('LD20260917-001-D','channel_deduction','渠道扣款-LE20260917-001','主播承诺不符，按退款额 100% 向渠道（主播/机构）扣款：119.80 元（退款基数 119.80 元）。',119.80,'复盘同步')
		 ) AS d(doc,dt,ti,ct,am,ex)
		 WHERE e.entry_no='LE20260917-001' ON CONFLICT (doc_no) DO NOTHING`,
		`INSERT INTO live_return_disposals(doc_no,entry_id,dtype,title,content,amount,status,executed_by,executed_at)
		 SELECT d.doc,e.id,d.dt,d.ti,d.ct,d.am,'done',d.ex,'2026-09-17 17:00+08' FROM live_return_orders e
		 CROSS JOIN (VALUES
		   ('LD20260917-002-R','refund','退款-LE20260917-002','向消费者退款 59.90 元（1 盒 × 59.90 元/盒），订单 OD20260916-006，原因：物流延迟。',59.90::numeric,'finance'),
		   ('LD20260917-002-S','restock','库存退回-LE20260917-002','退回商品 1 盒 / 0.25 kg（成品 FG20260912-01 蒜香虾仁）登记回厂，入「直播退货仓」隔离待判。',0.25,'warehouse'),
		   ('LD20260917-002-C','script','客服话术-LE20260917-002','您好，您的订单因冷链物流延迟（实际到货约 60.0 小时，超出承诺 48 小时）影响了体验，我们已为您退款 59.90 元，商品无需寄回。我们已同步启动对承运商的追责流程，抱歉久等。',0.00,'quality'),
		   ('LD20260917-002-D','channel_deduction','渠道扣款-LE20260917-002','非渠道责任（物流/工厂质量），渠道扣款 0 元：0.00 元（退款基数 59.90 元）。',0.00,'复盘同步')
		 ) AS d(doc,dt,ti,ct,am,ex)
		 WHERE e.entry_no='LE20260917-002' ON CONFLICT (doc_no) DO NOTHING`,
		// 批次②退回库存（直播退货仓 0.75kg 隔离待判）与回厂登记
		`INSERT INTO return_goods(return_no,finished_batch_id,box_no,qty_kg,note,live_entry_id)
		 SELECT 'RT20260917-01',f.id,'直播退货-LE20260917-001',0.50,'直播集中退货退回（LE20260917-001），隔离待判',e.id
		 FROM finished_batches f, live_return_orders e
		 WHERE f.batch_no='FG20260912-01' AND e.entry_no='LE20260917-001'
		 ON CONFLICT (return_no) DO NOTHING`,
		`INSERT INTO return_goods(return_no,finished_batch_id,box_no,qty_kg,note,live_entry_id)
		 SELECT 'RT20260917-02',f.id,'直播退货-LE20260917-002',0.25,'直播集中退货退回（LE20260917-002），隔离待判',e.id
		 FROM finished_batches f, live_return_orders e
		 WHERE f.batch_no='FG20260912-01' AND e.entry_no='LE20260917-002'
		 ON CONFLICT (return_no) DO NOTHING`,
		`INSERT INTO inventory(finished_batch_id,location,qty_kg,status)
		 SELECT id,'直播退货仓',0.75,'quarantined' FROM finished_batches WHERE batch_no='FG20260912-01'
		 ON CONFLICT DO NOTHING`,
		// 批次②渠道复盘（已同步）+ 下一场备货建议
		`INSERT INTO channel_reviews(review_no,batch_id,channel_id,promise_summary,delivery_summary,sample_summary,
		   suggestion,suggestion_detail,refund_total,deduction_amount,restock_adjustment,status,reviewed_by,reviewed_at,synced_at)
		 SELECT 'RV20260917-01',b.id,c.id,
		        '主播承诺：全场顺丰冷链 48 小时送达；坏单包赔；缺斤少两退一赔三（承诺到货时效 48 小时）',
		        '实际到货时效均值 52.5 小时 / 最高 60.0 小时，1/2 单超出承诺 48 小时',
		        '留样检测：SP-001 合格；退货登记中批次质量异常 0 笔、送检不合格 0 笔',
		        'channel_deduction',
		        '主播承诺不符退货 1 笔（退款 119.80 元），按退款额 100% 向渠道（主播/机构）扣款 119.80 元；承诺文案须整改。另物流延迟 1 单（60h>48h），建议同步向承运商追责。',
		        179.70,119.80,
		        '下一场直播备货量按本场 -20% 控制，主播承诺文案须经品控审核后上架。',
		        'synced',3,'2026-09-17 18:00+08','2026-09-17 18:30+08'
		 FROM live_return_batches b, channels c
		 WHERE b.batch_no='LRB20260917-01' AND c.code='CH03'
		 ON CONFLICT (review_no) DO NOTHING`,
		`INSERT INTO live_restock_advice(finished_batch_id,session_id,review_id,direction,adjust_pct,advice,updated_at)
		 SELECT f.id,ls.id,rv.id,'down',20,
		        '下一场直播备货量按本场 -20% 控制，主播承诺文案须经品控审核后上架。','2026-09-17 18:30+08'
		 FROM finished_batches f, live_sessions ls, channel_reviews rv
		 WHERE f.batch_no='FG20260912-01' AND ls.session_no='LS20260916-02' AND rv.review_no='RV20260917-01'
		 ON CONFLICT (finished_batch_id) DO NOTHING`,
	}
	for _, s := range stmts {
		if _, err := p.Exec(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
