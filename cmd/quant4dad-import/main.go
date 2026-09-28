// quant4dad-import imports user-supplied CSV files into an initialized SQL store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "Import failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("quant4dad-import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "local YAML configuration (same file as the API)")
	instrumentPath := flags.String("instruments", "", "instrument metadata CSV path")
	barPath := flags.String("bars", "", "OHLCV CSV path")
	dryRun := flags.Bool("dry-run", false, "validate input and existing-data conflicts without writing")
	replace := flags.Bool("replace", false, "explicitly allow replacement of differing keys supplied in these files")
	timeout := flags.Duration("timeout", 5*time.Minute, "total import deadline, up to 30m")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *instrumentPath == "" && *barPath == "" {
		return errors.New("provide -instruments and/or -bars; see docs/data-import.md")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("timeout must be positive and at most 30m")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if cfg.Storage.Backend != config.StorageBackendSQLite && cfg.Storage.Backend != config.StorageBackendMySQL {
		return errors.New("CSV import supports SQLite and MySQL storage only; CSV is the input format")
	}
	// A dry run must not create an empty SQLite database or run any migrations.
	if cfg.Storage.Backend == config.StorageBackendSQLite {
		stat, err := os.Stat(cfg.Storage.SQLite.Path)
		if err != nil || !stat.Mode().IsRegular() {
			return errors.New("SQLite database does not exist; start the API or run its migration first")
		}
	}
	request := service.CSVImportRequest{DryRun: *dryRun, Replace: *replace}
	for _, input := range []struct {
		path   string
		label  string
		target *io.Reader
	}{{*instrumentPath, "instruments", &request.Instruments}, {*barPath, "bars", &request.Bars}} {
		if input.path == "" {
			continue
		}
		f, err := openInput(input.path)
		if err != nil {
			return errors.New(input.label + " CSV must be a readable regular UTF-8 file no larger than 32 MiB")
		}
		defer f.Close()
		*input.target = f
	}
	if err := logger.Init(logger.Config{Level: cfg.Log.Level, Stderr: true}); err != nil {
		return errors.New("logger initialization failed")
	}
	defer logger.Shutdown()
	db, err := repository.Open(cfg.Storage)
	if err != nil {
		return errors.New("cannot open the configured database; check its connection settings")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("cannot access the database connection")
	}
	defer sqlDB.Close()
	if err := sqlDB.PingContext(ctx); err != nil {
		return errors.New("cannot connect to the configured database")
	}
	importer := service.NewCSVImportService(repository.NewCSVImportRepository(db))
	result, err := importer.Import(ctx, request)
	if err != nil {
		var validation *service.CSVImportError
		var conflict *service.CSVImportConflictError
		if errors.As(err, &validation) || errors.As(err, &conflict) || errors.Is(err, repository.ErrCSVImportSchema) || errors.Is(err, repository.ErrCSVImportEngine) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errors.New("database import failed; check database logs and run -dry-run before retrying")
	}
	data, err := sonic.Marshal(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func openInput(path string) (*os.File, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > service.MaxCSVImportBytes {
		return nil, errors.New("invalid CSV file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stat, err = f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > service.MaxCSVImportBytes {
		f.Close()
		return nil, errors.New("invalid CSV file")
	}
	return f, nil
}
