# Data-source extension interfaces

This distribution includes no market-data crawlers or news-feed clients. It can query and analyze data already stored in SQLite/MySQL, including data imported with `quant4dad-import` (see `docs/data-import.md`). The API starts with empty registries; starting a market-data sync returns an unavailable error before creating a task.

To install your own data integration, implement `datasource.Client` (`Name`, `ListInstruments`, `FetchBars`), register its constructor with `datasource.Register`, then blank-import that package from `providers/providers.go`. Choose the registered name in `datasource.provider`. No provider is selected implicitly when several are registered.

- Use canonical instrument codes such as `sh.600000`, `sz.000001` and `bj.430001`.
- Return bars in ascending date order within the requested inclusive range.
- `InstrumentBarsClient` optionally receives persisted asset metadata for routing.
- `RequestLimitedClient` optionally installs the shared limiter on every outgoing request. Other clients are rate-limited once per public call.
- Preserve adjustment provenance accurately; a positive factor alone does not establish a trusted source. Manual CSV import always records `manual_import`.
- Wrap fetch failures with `NewFetchError` when useful, after removing credentials and private data from any diagnostic body.

For your own news integration, implement `NewsSource` (`Name`, `Fetch`) and call `RegisterNewsSource`. `SeenFunc`, `Paginate`, `ClampTitle` and the Shanghai timezone helper remain available. With no registered news sources, the collector has no feeds to fetch. User events can also enter pipelines through `POST /api/v1/events/ingest/:pipelineId`.

Only enable automatic synchronization/news collection after installing and configuring your integration. Existing fake-provider contract tests remain available; no test depends on a live feed.
