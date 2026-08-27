package launcher

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

type StarsTransactionLister interface {
	ListStarTransactions(context.Context, string, int, int) ([]StarTransaction, error)
}

type StarsReconcileOptions struct {
	EnvPath   string
	BaseDir   string
	Out       io.Writer
	LookupEnv func(string) (string, bool)
	Client    StarsTransactionLister
	MaxRows   int
	PageSize  int
}

func DefaultStarsReconcileOptions() StarsReconcileOptions {
	return StarsReconcileOptions{
		EnvPath: ".env", BaseDir: ".", Out: os.Stdout, LookupEnv: os.LookupEnv,
		Client: NewTelegramClient(15 * time.Second), MaxRows: 500, PageSize: 100,
	}
}

// RunStarsReconcileCLI exposes only bounded page controls. The window always
// starts at the newest provider row so LocalOnly and completion share scope.
func RunStarsReconcileCLI(ctx context.Context, args []string, opts StarsReconcileOptions) int {
	fs := flag.NewFlagSet("reconcile-stars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	maxRows := fs.Int("max-rows", opts.MaxRows, "maximum provider rows to inspect")
	pageSize := fs.Int("page-size", opts.PageSize, "provider page size (1-100)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *maxRows < 1 || *maxRows > 100000 || *pageSize < 1 || *pageSize > 100 {
		out := opts.Out
		if out == nil {
			out = os.Stdout
		}
		fmt.Fprintln(out, "Stars reconciliation: invalid arguments")
		return 2
	}
	opts.MaxRows = *maxRows
	opts.PageSize = *pageSize
	return RunStarsReconcile(ctx, opts)
}

// RunStarsReconcile performs a bounded, read-only comparison. It prints only
// aggregate counts and returns 1 when operator review is required.
func RunStarsReconcile(ctx context.Context, opts StarsReconcileOptions) int {
	defaults := DefaultStarsReconcileOptions()
	if opts.EnvPath == "" {
		opts.EnvPath = defaults.EnvPath
	}
	if opts.BaseDir == "" {
		opts.BaseDir = filepath.Dir(opts.EnvPath)
	}
	if opts.Out == nil {
		opts.Out = defaults.Out
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = defaults.LookupEnv
	}
	if opts.Client == nil {
		opts.Client = defaults.Client
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = defaults.MaxRows
	}
	if opts.PageSize <= 0 || opts.PageSize > 100 {
		opts.PageSize = defaults.PageSize
	}

	values, _, err := loadEnvironment(opts.EnvPath, opts.LookupEnv)
	if err != nil {
		fmt.Fprintln(opts.Out, "Stars reconciliation: configuration error")
		return 1
	}
	cfg, err := config.LoadFromMap(values)
	if err != nil {
		fmt.Fprintln(opts.Out, "Stars reconciliation: configuration error")
		return 1
	}
	dbPath := cfg.DBPath
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(opts.BaseDir, dbPath)
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintln(opts.Out, "Stars reconciliation: database error")
		return 1
	}
	defer db.Close()

	var providerRows []storage.ProviderTransaction
	complete := false
	for offset := 0; offset < opts.MaxRows; offset += opts.PageSize {
		limit := opts.PageSize
		if remaining := opts.MaxRows - offset; remaining < limit {
			limit = remaining
		}
		page, err := opts.Client.ListStarTransactions(ctx, cfg.BotToken, offset, limit)
		if err != nil {
			fmt.Fprintln(opts.Out, "Stars reconciliation: Telegram API error")
			return 1
		}
		for _, row := range page {
			if normalized, ok := normalizeStarTransaction(row); ok {
				providerRows = append(providerRows, normalized)
			}
		}
		if len(page) < limit {
			complete = true
			break
		}
	}
	// A full final page is ambiguous: it may be the exact end or a truncated
	// window. Probe one row beyond the cap without processing it.
	if !complete {
		probe, err := opts.Client.ListStarTransactions(ctx, cfg.BotToken, opts.MaxRows, 1)
		if err != nil {
			fmt.Fprintln(opts.Out, "Stars reconciliation: Telegram API error")
			return 1
		}
		complete = len(probe) == 0
	}
	report, err := storage.NewSQLPaymentLedgerStore(db).Reconcile(ctx, storage.PaymentMethodStars, providerRows, complete)
	if err != nil {
		fmt.Fprintln(opts.Out, "Stars reconciliation: database error")
		return 1
	}
	fmt.Fprintf(opts.Out,
		"Stars reconciliation: rows=%d matched=%d provider_only=%d local_only=%d amount_mismatch=%d duplicates=%d needs_review=%d complete=%t\n",
		report.ProviderRows, report.Matched, report.ProviderOnly,
		report.LocalOnly, report.AmountMismatch, report.DuplicateRows, report.NeedsReview, report.WindowComplete)
	if !report.WindowComplete || report.ProviderOnly > 0 || report.LocalOnly > 0 || report.NeedsReview > 0 || report.DuplicateRows > 0 {
		return 1
	}
	return 0
}

func normalizeStarTransaction(row StarTransaction) (storage.ProviderTransaction, bool) {
	kind := ""
	invoicePayload := ""
	var payerID int64
	amount := row.Amount
	if row.Receiver != nil && row.Receiver.Type == "user" && row.Receiver.TransactionType == "invoice_payment" {
		kind = storage.PaymentEventRefunded
		invoicePayload = row.Receiver.InvoicePayload
		payerID = row.Receiver.User.ID
	} else if row.Source != nil && row.Source.Type == "user" && row.Source.TransactionType == "invoice_payment" {
		kind = storage.PaymentEventCaptured
		invoicePayload = row.Source.InvoicePayload
		payerID = row.Source.User.ID
	}
	if kind == "" {
		return storage.ProviderTransaction{}, false
	}
	if amount < 0 {
		amount = -amount
	}
	orderID, payloadErr := strconv.ParseInt(invoicePayload, 10, 64)
	var occurredAt time.Time
	if row.Date > 0 {
		occurredAt = time.Unix(row.Date, 0).UTC()
	}
	return storage.ProviderTransaction{
		Provider: storage.PaymentMethodStars, Kind: kind, ExternalID: row.ID,
		OrderID: orderID, PayloadValid: payloadErr == nil && orderID > 0,
		AmountMinor: amount, Currency: "XTR", Scale: 0,
		NanostarAmount: row.NanostarAmount,
		PayerID:        payerID, OccurredAt: occurredAt,
	}, true
}
