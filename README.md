# 海鲜预制菜工厂 · 批次解冻加工与召回服务

基于 **Go 1.23 + PostgreSQL 16 + Redis 7** 的海鲜预制菜（虾仁 / 扇贝 / 鱿鱼 / 鱼柳）工厂
批次解冻加工、全链追溯与召回处置系统。覆盖「采购入库 → 仓储温控 → 解冻加工 → 留样检测 →
渠道发货 → 品控事件 → 精准召回 → 退货三态处置 → 客服赔付 / 供应商追偿 → 监管抽样」全流程，
并支持 **直播间集中退货**：四类原因区分、退款/库存退回/客服话术/渠道扣款分别生成、
三源对比复盘（主播承诺 × 实际到货时效 × 批次检测）、渠道复盘同步与下一场直播备货建议。

---

## 原始需求

实现海鲜预制菜工厂批次解冻加工与召回服务，可使用 Go、PostgreSQL 和 Redis。工厂接收虾仁、扇贝、鱿鱼和鱼柳冻品时，服务记录供应商、捕捞或生产日期、入库温度、检疫证明、箱号和库位；生产计划下发后，解冻间记录解冻开始时间、中心温度、用水批次和操作人，加工线继续记录清洗、切配、调味、称重、包装、金检、留样和成品冷库。若出现原料温度超标、解冻超时、调味料批次用错、金检报警、包装漏气、渠道反馈异味、直播间集中退货或监管要求召回，服务要把采购入库、仓储温控、生产记录、品控结论、渠道反馈、客服赔付和财务追偿关联到原料批次和成品批次。召回时，系统需要定位已发货渠道、库存、已售订单和留样检测结果，生成停售、退货、赔偿和供应商追责材料。服务还要支持多条生产线并行、渠道定制包装、冷库盘点、供应商扣款和监管抽样。工厂管理者需要按原料批次看到它进入哪些成品、发往哪些渠道、是否留样合格，召回时才能精准到箱和订单。退货回厂后还要区分可复检、可返工和必须销毁的库存，避免召回后库存状态失真。

## 本期新增需求（直播间集中退货）

> 加入直播间集中退货。某场直播销售的海鲜预制菜集中退货时，服务要区分主播承诺不符、物流延迟、口味问题和批次质量异常。退款、库存退回、客服话术和渠道扣款分别生成。直播退货结束后，服务会对比主播承诺、实际到货时效和批次检测，形成渠道扣款、质量整改或物流追责建议。渠道复盘同步更新。复盘结果会影响下一场直播备货。

---

## 一、快速启动（Docker 一键部署）

前置：已安装 Docker 与 Docker Compose（宿主 daemon 通过 `DOCKER_HOST` 提供亦可）。

```bash
# 1) 准备环境变量（可直接复制示例）
cp .env.example .env
#    .env 中 CC_PUBLISH_PORT 为宿主机发布端口，避免并行任务端口冲突

# 2) 一键启动 app + PostgreSQL + Redis
docker compose up -d --build

# 3) 查看健康状态
docker compose ps
curl -fsS http://localhost:${CC_PUBLISH_PORT:-3120}/healthz
# => {"status":"ok"}

# 4) 浏览器访问（实际映射端口以命令输出为准）
#    http://<宿主IP>:3120
docker compose port app 8080

# 5) 验证结束，释放资源
docker compose down
```

- 仅 **app 服务**发布宿主机端口（`${CC_PUBLISH_PORT}:8080`）；PostgreSQL / Redis
  **不发布宿主端口**，只在 compose 内网通过服务名 `db` / `redis` 互访。
- 数据库表结构由服务启动时自动迁移（schema 内嵌于二进制），并幂等写入演示数据，
  重复启动不会重复播种。

**验证方式 = 宿主 `docker compose up -d` 健康 + 浏览器/接口关键业务流走通**（非本地裸跑）。

---

## 本地开发（无 Docker，Go + PostgreSQL + Redis 直连）

已有 Go 1.23 工具链、PostgreSQL、Redis 时，可不经容器直接运行：

```bash
# 1) 准备依赖：建库建角色（按实际环境调整）
psql -c "CREATE ROLE seafood LOGIN PASSWORD 'seafood_pw' SUPERUSER;"
psql -c "CREATE DATABASE seafood OWNER seafood;"
# Redis 使用默认 127.0.0.1:6379 即可

# 2) 启动服务（schema 自动迁移、演示数据幂等播种）
PGHOST=127.0.0.1 PGPORT=5432 PGUSER=seafood PGPASSWORD=seafood_pw \
PGNAME=seafood REDIS_ADDR=127.0.0.1:6379 APP_PORT=8080 go run ./cmd/server

# 3) 健康检查 + 登录
curl -fsS http://localhost:8080/healthz        # => {"status":"ok"}
curl -s -c /tmp/ck -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"manager","password":"manager123"}'
curl -s -b /tmp/ck http://localhost:8080/api/live-returns
```

浏览器访问 `http://localhost:8080` → 「直播退货」页即可操作全流程；前端为原生 HTML/CSS/JS，
无构建步骤，改完 `internal/api/web/` 刷新即可（资源嵌入二进制，需重启进程）。

### 本次实际验证记录（本地环境：Go 1.23.4 + PostgreSQL 15 + Redis 7）

- `go build ./...` / `go vet ./...` 全部通过；前端 `node --check app.js` 语法通过。
- 干净库首次启动：schema 迁移与幂等播种成功，`/healthz` 返回 ok；**重复启动不产生重复数据**。
- 用 Python/curl 脚本对直播退货主流程跑了 **47 项断言全部通过**，覆盖：
  未登录 401、四类原因分类、每笔四项处置分别生成（退款额/渠道扣款率：承诺不符 100%、口味 30%、
  物流与质量异常 0）、批次质量异常自动生成 critical 品控事件、处置越权 403/重复执行 409、
  退款后订单转已赔付、库存退回生成回厂登记并入「直播退货仓」隔离、三源对比复盘文案与建议判定、
  场次状态流转、复盘后禁写、复盘同步（扣款台账生效、反馈已处理、备货建议 pause/-20% 不互相覆盖）、
  仪表盘 KPI 联动。
- 既有流程回归：直播自动事件可直接发起召回（圈定原料/成品/订单/库存并生成四类材料），
  直播退回货物在「退货处置」判定「必须销毁」后进入销毁区库存。
- 说明：本环境无 Docker daemon，未执行 `docker compose up` 实跑；compose 配置沿用原工程未改动。

---

## 二、测试账号（逐角色：用户名 / 密码 / 权限）

| 用户名 | 密码 | 角色 | 权限范围 |
| --- | --- | --- | --- |
| `admin` | `admin123` | 系统管理员 | 全部功能与全部写操作 |
| `manager` | `manager123` | 工厂经理 | 全部业务读写（召回、退货判定、财务确认、直播复盘） |
| `quality` | `quality123` | 品控主管 | 品控事件、留样检测、发起召回、退货判定、监管抽样、渠道反馈、直播退货复盘与同步 |
| `warehouse` | `ware123` | 冷库仓管 | 冻品入库、补测温、发货出库、退货登记、冷库盘点、直播退货登记与库存退回 |
| `production` | `prod123` | 产线班长 | 下发计划、解冻记录、加工七步、完工入冷库、发货、订单、新建直播场次 |
| `finance` | `finance123` | 财务专员 | 消费者赔付支付、供应商扣款确认/扣款、直播退款与渠道扣款执行 |

登录态使用 Redis 会话（HttpOnly Cookie，有效期 8 小时）。越权调用返回 `403`。

---

## 三、内置演示数据

- 供应商：`S001 荣成远洋`（虾仁）、`S002 大连獐子岛`（扇贝）、`S003 舟山鱿鱼合作社`（鱿鱼）、`S004 北海鱼柳`（鱼柳）。
- 原料批次：
  - `RM20260901` 虾仁，入库 -18.2℃ 正常；
  - `RM20260902` 扇贝 -17.8℃；
  - **`RM20260903` 鱿鱼，入库 -15.1℃ 超过冻品限值 -18℃，自动锁定**，且已存在开放事件 `EV20260903-01`；
  - `RM20260904` 鱼柳 -18.5℃。
- 两条生产线并行：`L1 一号解冻加工线`、`L2 二号解冻加工线`；调味料批次 `T001 蒜香`、`T002 麻辣`。
- 计划/成品：
  - `PP20260910-01 → FG20260912-01 蒜香虾仁`（直播间 250g 彩包，留样 SP-001 合格）；
  - `PP20260910-02 → FG20260912-02 麻辣鱿鱼花`（商超 500g 简包，使用问题鱿鱼原料，解冻 290 分钟已超时，留样 SP-002 合格）。
- 渠道：`CH01 盒马(商超)`、`CH02 天猫(电商)`、`CH03 抖音直播间`、`CH04 餐饮经销`。
- 发货单 `SH20260913-01/02`，已售订单 `OD20260914-001/002/003、OD20260915-010、OD20260916-005/006`（精确到箱号）。
- 渠道反馈：直播间就 `FG20260912-02` 反馈异味。
- 冷库库存：FG20260912-01 余 115kg、FG20260912-02 余 100kg；另有「直播退货仓」隔离 0.75kg（历史直播退回）。
- **直播场次**：
  - `LS20260914-01 开海节直播专场`（主播「海味小厨」，承诺 48h 冷链送达/坏单包赔/退一赔三，**集中退货进行中**）；
  - `LS20260916-02 开海节返场直播`（**已复盘结案**）。
- **集中退货批次**：
  - `LRB20260915-01`（进行中，麻辣鱿鱼花）：已登记 `LE20260915-001 批次质量异常`（送检不合格）、
    `LE20260915-002 口味问题`，两笔各生成退款/库存退回/客服话术/渠道扣款四项**待执行**处置；
  - `LRB20260917-01`（已结案，蒜香虾仁）：`LE20260917-001 主播承诺不符`（赠品未随箱，渠道扣款 100%）、
    `LE20260917-002 物流延迟`（60h 超承诺 48h），四项处置均已执行。
- **渠道复盘**：`RV20260917-01` 已同步（建议=渠道扣款 119.80 元），并生成下一场备货建议
  「蒜香虾仁备货 -20%」（`live_restock_advice`）。

### 建议演示路径（端到端走一遍召回闭环）

1. 「原料入库」→ 打开 `RM20260903` 的**正向追溯**：可见它进入成品 `FG20260912-02`、发往直播渠道、已售订单与留样结果。
2. 「品控事件」→ 对 `EV20260903-01` **发起召回**：系统自动圈定关联原料/成品、锁定批次、隔离在库库存、标记已发货渠道与已售订单（精确到箱），并一次性生成**停售通知 / 退货指令 / 消费者赔偿 / 供应商追责**四类材料，同时生成赔付与供应商扣款台账。
3. 「召回管理」→ 打开召回档案查看 9 个板块与全部材料。
4. 「退货处置」→ 登记退货回厂，分别判定 **可复检 / 可返工 / 必须销毁**，观察冷库库存按状态独立流转。
5. 「财务追偿」→ 支付消费者赔付、确认供应商扣款。
6. 「监管抽样」→ 登记抽样并录入「不合格」，系统自动生成「监管要求召回」事件，可再次发起召回。

### 直播集中退货演示路径（本期新增）

1. 「直播退货」→ 查看进行中的集中退货批次 `LRB20260915-01`：两笔退货（批次质量异常 / 口味问题）各自已**分别生成退款、库存退回、客服话术、渠道扣款**四项待执行处置；批次质量异常那笔还自动升级了 critical 品控事件（可直接发起召回）。
2. 点「登记退货」再补两笔：**物流延迟**（填实际到货时效 52h，关联订单 `OD20260914-003`）、**主播承诺不符**（赠品未随箱，渠道扣款自动按退款额 100% 生成）。
3. 打开批次「明细/处置」，按角色逐项执行四项处置：财务执行**退款**（订单转已赔付）与**渠道扣款**、仓管执行**库存退回**（自动生成回厂登记并入「直播退货仓」隔离，可在「退货处置」再三态判定）、品控执行**客服话术**。
4. 点「退货结束·生成复盘」：系统对比**主播承诺 / 实际到货时效 / 批次检测**，因含批次质量异常，建议为**质量整改**并给出「暂停鱿鱼花备货」的下场备货建议。
5. 点「同步渠道复盘」：渠道复盘更新为已同步、渠道扣款台账生效、渠道反馈标记已处理，**下一场直播备货建议**面板同步更新（鱿鱼花暂停备货、虾仁维持 -20%）。
6. 「新建直播场次」创建下一场直播时，即可参考备货建议调整承诺时效与备货量。

---

## 四、功能与业务规则映射

| 需求点 | 实现 |
| --- | --- |
| 冻品接收记录供应商/捕捞生产日期/入库温度/检疫证明/箱号/库位 | 原料批次入库登记；入库温度 > -18℃ 自动锁定并写温控报警 |
| 仓储温控 | 随时补测温，超标自动锁定原料批次 |
| 解冻记录（开始时间/中心温度/用水批次/操作人） | 解冻记录；**超过 240 分钟自动生成「解冻超时」事件**（数据库生成列计算时长） |
| 清洗/切配/调味/称重/包装/金检/留样/成品冷库 | 加工七步留痕；调味批次与计划指定不一致→「调味料用错」；金检不合格→「金检报警」；包装不合格→「包装漏气」 |
| 多条生产线并行 | 生产线档案，计划绑定产线，L1/L2 并行 |
| 渠道定制包装 | 计划与成品批次记录包装规格（如直播间 250g 彩包、商超 500g 简包） |
| 原料 ↔ 成品双向批次关联 | `batch_links` 支持正反向追溯（一料多品、一品多料） |
| 按原料批次看成品/渠道/留样 | 原料正向追溯视图 |
| 召回定位渠道/库存/订单/留样 | 召回事务一次圈定：发货单、在库库存、已售订单（精确到箱号）、留样结果 |
| 停售/退货/赔偿/供应商追责材料 | 召回自动生成四类 `recall_actions` 文书（含编号、抬头、正文、金额） |
| 客服赔付 / 财务追偿 | `customer_compensations`（支付后订单转已赔付）、`supplier_deductions`（待确认→已确认→已扣款） |
| 退货回厂区分可复检/可返工/必须销毁 | 三态处置并同步库存：隔离待判 / 返工区 / 销毁区，避免库存状态失真 |
| 冷库盘点 | 按库位快照系统库存生成盘点单与明细 |
| 监管抽样 | 原料/成品抽样登记，**抽检不合格自动生成监管召回事件** |
| 渠道反馈异味 / 直播集中退货 | 反馈登记自动生成对应等级品控事件 |
| 直播集中退货四类原因区分 | `live_return_orders.reason_cat`：主播承诺不符 / 物流延迟 / 口味问题 / 批次质量异常 |
| 退款/库存退回/客服话术/渠道扣款分别生成 | 每笔退货各生成一条 `live_return_disposals`（refund/restock/script/channel_deduction），按角色分别执行 |
| 渠道扣款规则 | 承诺不符按退款额 100%、口味问题按 30%、物流延迟与批次质量异常为 0（分别追责物流/工厂） |
| 退货结束三源对比复盘 | 对比主播承诺、实际到货时效（超承诺单统计）、批次留样/送检；建议：渠道扣款/质量整改/物流追责 |
| 渠道复盘同步 | 复盘同步后扣款台账生效、反馈标记已处理；批次质量异常自动升级 critical 品控事件衔接召回 |
| 复盘结果影响下场备货 | `live_restock_advice` 按成品批次 upsert：质量整改→暂停备货、承诺不符→下调 20%、物流追责→维持并放宽时效承诺 |
| 直播退回库存衔接三态处置 | 执行「库存退回」自动生成 `return_goods` 并入「直播退货仓」隔离，随后判定 可复检/可返工/必须销毁 |

库存状态机：成品库存 `available → quarantined（召回隔离）→ reworked / destroyed（退货处置）`，
账面可用量与隔离/返工/销毁量分开统计，召回不会冲销可用量数字造成失真。

---

## 五、HTTP API 一览（均在 `/api` 前缀下，除登录外需会话）

- 鉴权：`POST /auth/login`、`POST /auth/logout`、`GET /auth/me`
- 总览/档案：`GET /dashboard`、`/suppliers`、`/lines`、`/channels`、`/seasonings`
- 原料：`GET/POST /raw-batches`、`POST /raw-batches/{batchNo}/temp-logs`、`GET /raw-batches/{batchNo}/trace`
- 生产：`GET/POST /plans`、`POST /plans/{id}/thaw`、`POST /plans/{id}/process`、`POST /plans/{id}/finish`
- 成品：`GET /finished-batches`、`GET /finished-batches/{batchNo}/trace`、`POST /finished-batches/{batchNo}/sample`
- 渠道：`GET/POST /shipments`、`GET/POST /orders`、`GET/POST /feedback`
- 品控/召回：`GET/POST /events`、`GET/POST /recalls`、`GET /recalls/{id}`、`POST /recalls/{id}/close`
- 退货：`GET/POST /returns`、`POST /returns/{id}/dispose`
- 库存：`GET /inventory`、`GET/POST /stocktakes`
- 财务：`GET/POST /deductions`、`POST /deductions/{id}/status`、`GET/POST /compensations`、`POST /compensations/{id}/pay`
- 监管：`GET/POST /regulatory-samplings`、`POST /regulatory-samplings/{id}/result`
- 直播集中退货：
  - 场次：`GET/POST /live-sessions`、`GET /live-sessions/{id}`
  - 集中退货：`GET /live-returns`、`POST /live-returns`（开启）、`GET /live-returns/{id}`、
    `POST /live-returns/{id}/entries`（登记四类原因退货并生成四项处置）、
    `POST /live-returns/{id}/review`（退货结束·三源对比复盘）
  - 四项处置：`POST /live-entries/{id}/execute`（dtype=refund/restock/script/channel_deduction，按角色鉴权）
  - 渠道复盘：`GET /channel-reviews`、`GET /channel-reviews/{id}`、`POST /channel-reviews/{id}/sync`
  - 备货建议：`GET /live-restock-advice`

快速接口自测：

```bash
BASE=http://localhost:${CC_PUBLISH_PORT:-3120}
curl -s -c /tmp/ck -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"manager","password":"manager123"}'
curl -s -b /tmp/ck $BASE/api/raw-batches | head -c 400
curl -s -b /tmp/ck $BASE/api/raw-batches/RM20260903/trace | head -c 400
```

---

## 六、工程结构

```
.
├── cmd/server/main.go          # 入口：迁移、播种、启动 HTTP
├── internal/
│   ├── db/schema.sql           # 全部表结构（启动自动迁移）
│   ├── db/db.go / seed.go      # 连接重试 / 幂等演示数据
│   └── api/                    # chi 路由、Redis 会话、各业务处理器、内嵌前端
│       ├── handlers_live.go    # 直播间集中退货：登记/四项处置/三源复盘/渠道同步/备货建议
│       └── web/                # 单页前端（原生 HTML/CSS/JS，无构建步骤）
├── Dockerfile                  # 多阶段构建，非 root 用户 + HEALTHCHECK
├── docker-compose.yml          # app + postgres + redis，仅发布 app 端口
└── .env.example
```

安全与运维要点：多阶段 Dockerfile（golang:1.23-alpine 构建，alpine:3.20 运行）；
运行用户为非 root 的 `appuser`(uid 10001)；镜像内置 `HEALTHCHECK`；compose 中
PostgreSQL/Redis 均有 healthcheck，app 依赖两者健康后再启动；PostgreSQL 数据持久化到命名卷。
