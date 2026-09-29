# 数据源扩展

`Client` 是行情目录/历史接口，`InstrumentBarsClient` 在获取历史时保留股票与 ETF 身份，`Prober` 是有限只读连接检测。`providers` 包注册官方 Tushare API 和用户自行提供的 HTTP 服务契约；调用方通过 blank import 注册，只有明确配置 `provider: tushare` 或 `provider: http` 才启用。`manual` 和空值始终返回 `Unavailable`，不因注册数量自动选择网络源。

完整接口、字段、分页、权限、限流和凭据限制见 [docs/data-provider.md](../../../docs/data-provider.md)。CSV 导入独立于网络数据源，见 [docs/data-import.md](../../../docs/data-import.md)。发行版不包含第三方爬虫或内置新闻源。

自定义扩展可以在独立包 `init()` 中调用 `datasource.Register(name, constructor)` 或 `RegisterNewsSource`，并在 `providers` 加 blank import。构造器不得启动同步；错误不得泄漏上游凭据或响应体。多请求的客户端应实现 `RequestLimitedClient`，让每次实际 HTTP 请求经过注入的 limiter，包括分页和 probe。返回的 K 线须按日期升序，完整验证代码/周期/日期/价格/单位；未经官方验证的外部数据不得标记 `tushare.fund_adj`。
