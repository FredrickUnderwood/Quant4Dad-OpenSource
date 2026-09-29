# 行情数据源

未配置网络源时提示“在设置中选择并配置数据源或导入 CSV”。默认 `manual`（或未选择数据源）只使用已入库的数据和 CSV 导入，不会因为内置适配器已注册而自动访问网络。显式选择 `tushare` 或 `http` 后才启用对应接口。此发行版不包含第三方网站爬虫或新闻采集器。

Tushare 是官方 API；HTTP 是用户自行提供的数据服务契约，项目不提供或承诺任何免费第三方行情地址。切换数据源前，应确认已有标的、价格单位和数据使用权限符合自己的用途。

## Tushare 官方 API

使用用户自行申请的 Tushare token。适配器只向 `https://api.tushare.pro` 发送 POST JSON 请求，不使用 HTTP 降级，不允许用自定义 `base_url` 转发官方 token，也不跟随重定向。构造器仅接受空 `base_url` 或这个精确官方 HTTPS 地址（可带末尾 `/`）。测试中的服务器替换入口不对配置开放。

股票目录使用 `stock_basic`，按 SSE、SZSE、BSE 分区读取当前上市标的。股票 `1d`、`1w`、`1mo` 分别使用 `daily`、`weekly`、`monthly` 的原始未复权 OHLCV。历史查询按不超过一年、不重叠的日期窗口分段；返回结果按日期升序排列。没有根据价格反推复权因子。

启用 `include_etf` 后，ETF 目录使用 `etf_basic`，按 SH、SZ 分区读取当前上市的 ETF，保存官方资产类型和基金元数据。ETF 仅支持 `1d`，使用 `fund_daily`；`1w`、`1mo` 返回不支持错误。代码前缀或名称不会被当成 ETF 身份依据。独立获取某只 ETF 时，先向 `etf_basic` 核实其身份，已从本客户端官方目录核实的身份可复用。

ETF 原始日线与 `fund_adj` 按代码、日期精确连接。`fund_adj` 使用每页 1000 条及 `offset`；因子必须是有限正数。因子值 `1` 是合法官方值。缺失、重复、无效日期、错误代码或异常因子导致整次获取失败，不返回部分数据，不填充、不沿用相邻日期、不悄悄退回 `1`。只有这样取得的 ETF 因子标记为 `tushare.fund_adj`。股票行情不具有该标记。

Tushare 行情的 `vol`（手）转换为股/份：乘以 100；`amount`（千元）转换为元：乘以 1000。所有 OHLC 保留接口原始价格。因子用于下游明确请求的复权计算，不会直接改写原始 OHLC。

目录接口未声明通用 offset 分页能力，因此采用官方交易所分区；单个分区达到官方行数上限时按可能截断处理并失败，不能声称目录完整。当前适配器的保守阈值为 `stock_basic` 6000、`etf_basic` 5000。行情窗口同时校验接口上限（daily/weekly 6000、monthly 4500、fund_daily 5000）和日期范围可能容纳的最大日数。官方权限、积分要求、频率和数据可见范围由用户的 Tushare 账号决定，连接检测成功不代表拥有所有行情或 ETF 权限。

官方契约来源：

- [HTTP 调用说明](https://tushare.pro/document/1?doc_id=40)
- [股票列表 stock_basic](https://tushare.pro/document/2?doc_id=25)
- [股票日线 daily](https://tushare.pro/document/2?doc_id=27)、[周线 weekly](https://tushare.pro/document/2?doc_id=144)、[月线 monthly](https://tushare.pro/document/2?doc_id=145)
- [ETF 基础信息 etf_basic](https://tushare.pro/document/2?doc_id=385)
- [基金日线 fund_daily](https://tushare.pro/document/2?doc_id=127)、[基金复权因子 fund_adj](https://tushare.pro/document/2?doc_id=199)

## 自有 HTTP 数据服务

`base_url` 是用户明确配置的 `http://` 或 `https://` 服务地址，可含路径前缀，如 `https://data.example.org/market`。拒绝 URL 用户名/密码、query、fragment、相对地址和其它协议。不跟随任何重定向；配置 Bearer token 时，只有此地址下的两个固定路径会收到凭据。跨公网传输凭据时应使用 HTTPS；HTTP 用于用户明确配置的本地或受控网络服务。

可选 token 通过 `Authorization: Bearer <token>` 发送，不放入 URL。请求带 `Accept: application/json`。返回 HTTP 200 和 JSON，不能返回 SSE、JSONP 或 HTML。每次最多 1000 条。

### 目录

```http
GET <base_url>/instruments?cursor=&limit=1000
```

```json
{
  "items": [
    {
      "code": "sh.600000",
      "name": "Synthetic Example Stock",
      "asset_type": "stock",
      "exchange": "SH",
      "listed_date": "2020-01-02",
      "status": "active"
    }
  ],
  "next_cursor": ""
}
```

必填字段：`code`、非空 `name`、`asset_type`。代码格式为 `sh.`、`sz.`、`bj.` 加六位数字；资产类型只允许 `stock` 或 `etf`。ETF 仅允许 SH/SZ。`exchange` 若省略则由代码补出；若存在必须严格匹配 `SH`、`SZ`、`BJ`。`status` 省略或为空时为 `active`，否则允许 `active`、`delisted`、`pending`。

可选字段与限制如下，字符串不能含控制字符：

| 字段 | 约束 |
| --- | --- |
| `name` | 最多 64 字符 |
| `industry` | 最多 64 字符 |
| `listed_date`、`setup_date` | `YYYY-MM-DD`，省略/空字符串/null 表示未知 |
| `full_name`、`index_name` | 最多 255 字符 |
| `index_code`、`etf_type` | 最多 32 字符 |
| `manager`、`custodian` | 最多 128 字符 |
| `management_fee` | 可选有限非负数，或 null |

这些元数据是用户接口声明的数据，不构成 Tushare 官方身份或因子的证明。`include_etf` 同样适用于 HTTP 数据源：关闭时仍完整校验每页数据、检测重复并跟完分页，但返回目录会排除 ETF；启用后才纳入 ETF。应用按资产身份获取 ETF 时只支持日线，关闭开关或请求 ETF 周/月线会明确返回不支持且不发出行情请求。基础 `FetchBars` 没有资产身份参数，保持通用协议，不根据代码前缀猜测 ETF。

### 原始 K 线

```http
GET <base_url>/bars?code=sh.600000&period=1d&start=2024-01-02&end=2024-01-03&cursor=&limit=1000
```

```json
{
  "items": [
    {
      "code": "sh.600000",
      "period": "1d",
      "date": "2024-01-02",
      "open": 10,
      "high": 12,
      "low": 9,
      "close": 11,
      "volume": 100,
      "amount": 1100,
      "adj_factor": 1
    }
  ],
  "next_cursor": ""
}
```

`period` 仅允许 `1d`、`1w`、`1mo`。`start`、`end` 为包含两端的日历日期；响应 `date` 为该根 K 线的交易日期，周/月线通常是该周期最后交易日。日期按 UTC 零点存储，不包含时分秒或时区。查询日期限制为 1900–2200 年且跨度不超过 150 个年份。

除 `adj_factor` 外，例子中的所有字段必填，数值必须是 JSON number，不能是字符串或 null。OHLC 必须有限且大于 0，`low <= open/close <= high`；`volume` 为股/份，`amount` 为元，均为有限非负数。返回代码、周期和日期必须与请求匹配。

`adj_factor` 可省略或为 null，默认 `1`；提供时必须有限且大于 0。接口必须返回原始 OHLC，不能把已复权价格冒充原始价格。所有此适配器产生的记录标记为 `adj_source="http"`。响应中禁止提供 `adj_source`，包括试图提供 `tushare.fund_adj`；用户接口因子不会获得可信 ETF 因子标记。

### 分页与失败处理

两个响应必须始终包含数组 `items` 和字符串 `next_cursor`；没有更多数据时显式返回 `""`。非空 cursor 作为不透明字符串原样送回同一个路径，不能是跳转地址。即使一页恰好 1000 条，空 cursor 也明确表示结束；服务端必须保证没有遗漏。

cursor 最多 2048 字符且不能含控制字符。空页面不能带非空下一页 cursor；重复 cursor、重复标的代码、重复 K 线日期、超过 1000 条的单页、超过 1000 页或 200000 条总记录都会失败。顶层及记录未知字段、类型不符、非法价格、超范围日期也会失败。任何后续页面失败都丢弃本次累计结果，不会让部分数据作为一次成功同步入库。

## 安全限制、限流与连接检测

每个上游 HTTP 请求受 `rate_limit_per_min` 限流，包括目录分区、历史窗口、因子分页和连接检测；不是只对最外层的同步方法计一次。`<=0` 表示未启用本地频率限制，仍受官方账号配额约束。适配器不自动重试失败请求。

每次 HTTP 请求超时 30 秒，响应头等待上限 15 秒，响应体上限 8 MiB。错误只有固定分类，不保留上游响应体、请求 URL、token 或原始网络错误文本。调用方可用 `errors.Is` 区分 `datasource.ErrProviderConfig`、`ErrProviderAccess`、`ErrProviderRateLimit`、`ErrProviderTimeout`、`ErrProviderResponse`、`ErrProviderPagination`、`ErrProviderUpstream`、`ErrProviderInput`、`ErrProviderUnsupported`；主动取消保留 `context.Canceled`。HTTP 401/403 是权限错误，429 是限流错误；Tushare 官方 JSON code 2002 是权限错误，其他未明确公开分类的非零 code 为上游失败。

两个内置客户端都实现可选检测接口：

```go
client, err := datasource.New(cfg.Datasource)
if err != nil {
    return err
}
prober, ok := client.(datasource.Prober)
if !ok {
    return datasource.ErrProviderUnsupported
}
return prober.Probe(ctx)
```

HTTP 检测只读取、校验目录第一页（上限 1000），不继续 cursor。Tushare 检测仅请求 `stock_basic` 的 `ts_code=000001.SZ`，校验精确单条身份，不采集全市场。检测只验证该有限请求，不执行同步、不写行情，也不保证后续所有接口都已获授权。`manual`/空选项的 placeholder 同样实现 `Prober`，明确返回 `ErrNoProvider`。

扩展代码如需 ETF 历史，使用 `datasource.InstrumentBarsClient.FetchInstrumentBars` 并传入持久化资产身份。Tushare 的基础 `Client.FetchBars` 保留股票契约。应用的同步路径已优先调用带资产身份的接口。
