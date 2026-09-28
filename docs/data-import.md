# Import your own data

The open-source edition contains no bundled market-data or news crawlers. Import CSV files you supply into SQLite or MySQL, then use the existing Web, API, backtest and optional MCP tools to read them. The importer never contacts a data provider.

Start the API once to initialize the database schema.

## Docker installation

The installer mounts its API configuration at `/app/config/api.yaml` and the persistent data directory at `/app/data`. Use the importer already included in that API container so it reads the same configuration and database as the running service. From the repository root, with the default installer state directory:

```sh
mkdir -p data/standalone/data/import
cp examples/import/*.csv data/standalone/data/import/
docker compose --env-file data/standalone/deployment.env -f deploy/compose.yaml exec -T api /app/quant4dad-import -config /app/config/api.yaml -instruments /app/data/import/instruments.csv -bars /app/data/import/bars.csv -dry-run
```

For a fresh demonstration database, the preview should report 2 instruments and 160 bars to insert. After reviewing it, run the same command without `-dry-run`:

```sh
docker compose --env-file data/standalone/deployment.env -f deploy/compose.yaml exec -T api /app/quant4dad-import -config /app/config/api.yaml -instruments /app/data/import/instruments.csv -bars /app/data/import/bars.csv
```

If the installer used a custom state directory, substitute it for `data/standalone` in the host paths above. For your own data, copy your CSV files into that state's `data/import/` directory and use their `/app/data/import/` paths. The container command uses whichever SQLite or MySQL backend its mounted configuration selects. Do not use the repository's example `config/quant4dad.yaml` to target an installer-managed database from the host.

## Local Go process

For an API started directly on the host, build the CLI and use that API's actual local configuration. For the repository's example configuration:


```sh
go build -o ./bin/quant4dad-import ./cmd/quant4dad-import
./bin/quant4dad-import -config config/quant4dad.yaml -instruments examples/import/instruments.csv -bars examples/import/bars.csv -dry-run
./bin/quant4dad-import -config config/quant4dad.yaml -instruments examples/import/instruments.csv -bars examples/import/bars.csv
```

Use the same local configuration file as the API. Relative database and input paths are relative to the current working directory. The CLI requires an existing schema and never runs migrations; `-dry-run` does not create a database or change rows. This command supports `storage.backend: sqlite` or `mysql` (InnoDB tables required); CSV is the **input format**, not the storage backend. Configuration is local, with no Agenda or Nacos requirement.

The included sample is fabricated demonstration data for a stock and an ETF. It is not historical market data and must not be used for investment conclusions. It deliberately uses explicit sample names. For an existing populated database, start with your own files or a separate demonstration database to avoid key conflicts.

## Instruments

```csv
code,name,asset_type
sh.600000,Demo stock (synthetic),stock
sh.510300,Demo ETF (synthetic),etf
```

Required columns: `code`, `name`, `asset_type`. Supported optional columns: `industry`, `status`, `exchange`, `listed_date`.

- Codes use `sh.`, `sz.` or `bj.` followed by six digits. Names contain 1–64 characters. Asset type is `stock` or `etf`.
- New instruments default to `status=active` and exchanges `SSE`, `SZSE`, `BSE` according to the code. Explicit exchanges must match the code. Status is `active`, `delisted` or `pending`.
- Dates use `YYYY-MM-DD` (1900–2100). A blank optional `listed_date` clears it when replacement is authorized.
- CSV-omitted optional metadata is preserved on existing instruments. Other fields, such as ETF manager and index metadata, are always preserved.
- To import bars for an existing instrument, `-instruments` can be omitted. New codes must be provided in the instrument CSV.

## Bars

```csv
code,period,date,open,high,low,close,volume,amount,adj_factor
sh.600000,1d,2026-01-05,10,10.5,9.8,10.2,100000,1020000,1
```

Required columns: `code,period,date,open,high,low,close,volume,amount`. Optional: `adj_factor` (defaults to 1). Columns may be reordered; unknown or duplicate columns are rejected.

- Period is `1d`, `1w` or `1mo`; dates are normalized to UTC midnight. Weekly/monthly rows retain your chosen candle date; the importer does not aggregate or invent missing sessions.
- Prices and factors must be finite and positive; volume and amount finite and nonnegative. Each number must be at most `1e15`, and `low <= open/close <= high`.
- Supply raw OHLC prices, share/unit volume and currency turnover in consistent units. The importer does not convert units or apply corporate actions.
- Every imported bar gets `adj_source=manual_import`. The CSV cannot supply an `adj_source` column. Even supplied factors are **unverified**, so operations requiring trusted forward-adjustment provenance can reject these rows. Historical provider provenance remains readable on untouched data.

## Validation and replacement

Each UTF-8 CSV is limited to 32 MiB. One invocation accepts at most 10,000 instrument rows/distinct codes and 100,000 bar rows. CRLF and an initial UTF-8 BOM are accepted. Control characters, surrounding whitespace, malformed CSV, invalid dates/numbers, missing references and repeated keys within a file are rejected. Larger datasets can be split into several invocations; each invocation is one transaction.

The importer validates the complete input, checks existing records, then writes instruments and bars in one SQL transaction. Validation or write failures roll back the whole invocation. If the connection is lost during commit, recheck with `-dry-run` before retrying; the database commits the whole transaction or none of it. Identical existing records are counted as unchanged. A differing existing key fails by default, including a change in adjustment provenance. Review it with:

```sh
./bin/quant4dad-import -config config/quant4dad.yaml -bars my-bars.csv -replace -dry-run
./bin/quant4dad-import -config config/quant4dad.yaml -bars my-bars.csv -replace
```

`-replace` updates only the supplied instrument fields and supplied bar keys; it does not delete other records. Imported bars replace the entire OHLCV/factor/provenance content of each supplied key. A dry-run is a point-in-time check; the writing invocation repeats all checks under transaction locks. Concurrent conflicting inserts cause rollback rather than silently overwrite data.

Successful stdout is a JSON summary with `dry_run` and `inserted`, `updated`, `unchanged` counts for instruments and bars. Dry-run counts describe planned changes. Errors go to stderr and exit nonzero. The default deadline is five minutes; `-timeout` can change it up to 30 minutes. Ctrl-C cancels the transaction.

After import, search for your instrument in the Web, or call `GET /api/v1/instruments?keyword=sh.600000` and its bars endpoint. Existing coverage summaries refresh on their normal scan or manual coverage scan; import does not enqueue a network sync.
