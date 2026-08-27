package launcher

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

type PaymentReviewOptions struct {
	EnvPath     string
	BaseDir     string
	Out         io.Writer
	LookupEnv   func(string) (string, bool)
	StarsClient StarsTransactionLister
	MaxRows     int
	PageSize    int
}

func DefaultPaymentReviewOptions() PaymentReviewOptions {
	return PaymentReviewOptions{
		EnvPath: ".env", BaseDir: ".", Out: os.Stdout, LookupEnv: os.LookupEnv,
		StarsClient: NewTelegramClient(15 * time.Second), MaxRows: 500, PageSize: 100,
	}
}

// RunPaymentReview provides a redacted list plus preview-first, explicitly
// confirmed resolution. It never discovers or resolves targets implicitly.
func RunPaymentReview(ctx context.Context, args []string, opts PaymentReviewOptions) int {
	if len(args) == 0 {
		printPaymentReviewUsage(paymentReviewOut(opts))
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		printPaymentReviewUsage(paymentReviewOut(opts))
		return 0
	case "list":
		return runPaymentReviewList(ctx, args[1:], opts)
	case "resolve":
		return runPaymentReviewResolve(ctx, args[1:], opts)
	case "ingest-stars":
		return runPaymentReviewIngestStars(ctx, args[1:], opts)
	default:
		printPaymentReviewUsage(paymentReviewOut(opts))
		return 2
	}
}

func printPaymentReviewUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  telegram-shop-bot payment-review list --provider stars|crypto|unknown")
	fmt.Fprintln(out, "  telegram-shop-bot payment-review resolve --provider stars|crypto|unknown --order N [--event N|--anomaly N|--order-target N] --state STATE [--decision compensated|accepted_refund|dismissed] --actor NAME --reason TEXT [--apply --confirm-order N]")
	fmt.Fprintln(out, "  telegram-shop-bot payment-review ingest-stars --kind capture|refund --transaction ID --order N --actor NAME --reason TEXT [--apply --confirm-order N]")
}

func runPaymentReviewList(ctx context.Context, args []string, opts PaymentReviewOptions) int {
	fs := flag.NewFlagSet("payment-review list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "provider: stars, crypto, or unknown")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 ||
		(*provider != "stars" && *provider != "crypto" && *provider != storage.PaymentReviewProviderUnknown) {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: invalid list arguments")
		return 2
	}
	dbPath, ok := paymentReviewDBPath(opts)
	if !ok {
		return 1
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: database error")
		return 1
	}
	defer db.Close()
	cases, err := storage.NewSQLPaymentLedgerStore(db).ListPaymentReviews(ctx, *provider)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: database error")
		return 1
	}
	targetCount := 0
	for _, item := range cases {
		targetCount += len(item.Targets)
	}
	fmt.Fprintf(paymentReviewOut(opts), "Payment reviews: provider=%s cases=%d targets=%d\n", *provider, len(cases), targetCount)
	for _, item := range cases {
		events, anomalies, orderTarget, reasons := reviewTargetFields(item.Targets)
		fmt.Fprintf(paymentReviewOut(opts), "order=%d state=%s event_ids=%s anomaly_ids=%s order_target=%s reasons=%s\n",
			item.OrderID, safeReviewCode(item.PaymentState), joinReviewIDs(events),
			joinReviewIDs(anomalies), joinReviewIDs(orderTarget), strings.Join(reasons, ","))
	}
	if targetCount > 0 {
		return 1
	}
	return 0
}

func runPaymentReviewResolve(ctx context.Context, args []string, opts PaymentReviewOptions) int {
	fs := flag.NewFlagSet("payment-review resolve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "provider: stars, crypto, or unknown")
	orderID := fs.Int64("order", -1, "order id; 0 for an orphan anomaly")
	state := fs.String("state", "", "resulting payment state")
	decision := fs.String("decision", "", "explicit anomaly or neutral-import decision")
	actor := fs.String("actor", "", "operator identity")
	reason := fs.String("reason", "", "resolution reason")
	apply := fs.Bool("apply", false, "append the reviewed resolution")
	confirmOrder := fs.Int64("confirm-order", -1, "must exactly equal --order when applying")
	orderTarget := fs.Int64("order-target", 0, "exact order-level review target")
	var eventIDs, anomalyIDs reviewIDList
	fs.Var(&eventIDs, "event", "exact payment event id; repeatable")
	fs.Var(&anomalyIDs, "anomaly", "exact payment anomaly id; repeatable")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *orderID < 0 ||
		(*provider != "stars" && *provider != "crypto" && *provider != storage.PaymentReviewProviderUnknown) ||
		(*decision != "" && *decision != "compensated" && *decision != "accepted_refund" && *decision != "dismissed") ||
		strings.TrimSpace(*actor) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: invalid resolve arguments")
		return 2
	}
	resolution := storage.PaymentReviewResolution{
		OrderID: *orderID, Provider: *provider, EventIDs: eventIDs, AnomalyIDs: anomalyIDs,
		OrderTargetID: *orderTarget, Decision: *decision,
		Actor: *actor, Reason: *reason, ResultingPaymentState: *state,
	}
	dbPath, ok := paymentReviewDBPath(opts)
	if !ok {
		return 1
	}
	previewDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: database error")
		return 1
	}
	item, err := storage.NewSQLPaymentLedgerStore(previewDB).PreviewPaymentReviewResolution(ctx, resolution)
	_ = previewDB.Close()
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: target set changed or invalid")
		return 1
	}
	fmt.Fprintf(paymentReviewOut(opts),
		"Payment review preview: order=%d provider=%s events=%d anomalies=%d order_target=%t decision=%s remaining_other=%d current=%s result=%s\n",
		resolution.OrderID, resolution.Provider, len(resolution.EventIDs), len(resolution.AnomalyIDs),
		resolution.OrderTargetID > 0, safeReviewCode(resolution.Decision), item.RemainingTargets,
		safeReviewCode(item.PaymentState), safeReviewCode(resolution.ResultingPaymentState))
	if !*apply {
		fmt.Fprintf(paymentReviewOut(opts), "No changes applied; rerun with --apply --confirm-order=%d\n", resolution.OrderID)
		return 0
	}
	if *confirmOrder != resolution.OrderID {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: --confirm-order must exactly match --order")
		return 2
	}
	writeDB, err := storage.OpenReadWriteExisting(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: database error")
		return 1
	}
	defer writeDB.Close()
	if err := storage.NewSQLPaymentLedgerStore(writeDB).ResolvePaymentReview(ctx, resolution); errors.Is(err, storage.ErrPaymentNeedsReview) {
		fmt.Fprintf(paymentReviewOut(opts), "Payment review recorded: order=%d provider=%s; other provider targets remain\n",
			resolution.OrderID, resolution.Provider)
		return 1
	} else if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: target set changed or database error")
		return 1
	}
	targetCount := len(resolution.EventIDs) + len(resolution.AnomalyIDs)
	if resolution.OrderTargetID > 0 {
		targetCount++
	}
	fmt.Fprintf(paymentReviewOut(opts), "Payment review resolved: order=%d provider=%s targets=%d result=%s\n",
		resolution.OrderID, resolution.Provider, targetCount,
		safeReviewCode(resolution.ResultingPaymentState))
	return 0
}

func paymentReviewDBPath(opts PaymentReviewOptions) (string, bool) {
	_, dbPath, ok := loadPaymentReviewConfig(opts)
	return dbPath, ok
}

func loadPaymentReviewConfig(opts PaymentReviewOptions) (*config.Config, string, bool) {
	defaults := DefaultPaymentReviewOptions()
	if opts.EnvPath == "" {
		opts.EnvPath = defaults.EnvPath
	}
	if opts.BaseDir == "" {
		opts.BaseDir = filepath.Dir(opts.EnvPath)
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = defaults.LookupEnv
	}
	values, _, err := loadEnvironment(opts.EnvPath, opts.LookupEnv)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: configuration error")
		return nil, "", false
	}
	cfg, err := config.LoadFromMap(values)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Payment review: configuration error")
		return nil, "", false
	}
	dbPath := cfg.DBPath
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(opts.BaseDir, dbPath)
	}
	return cfg, dbPath, true
}

func paymentReviewOut(opts PaymentReviewOptions) io.Writer {
	if opts.Out != nil {
		return opts.Out
	}
	return os.Stdout
}

type reviewIDList []int64

func (ids *reviewIDList) String() string {
	values := make([]string, 0, len(*ids))
	for _, id := range *ids {
		values = append(values, strconv.FormatInt(id, 10))
	}
	return strings.Join(values, ",")
}

func (ids *reviewIDList) Set(raw string) error {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid review target")
	}
	*ids = append(*ids, id)
	return nil
}

func reviewTargetFields(targets []storage.PaymentReviewTarget) (events, anomalies, orderTarget []int64, reasons []string) {
	for _, target := range targets {
		switch target.Kind {
		case storage.PaymentReviewTargetEvent:
			events = append(events, target.ID)
		case storage.PaymentReviewTargetAnomaly:
			anomalies = append(anomalies, target.ID)
		case storage.PaymentReviewTargetOrder:
			orderTarget = append(orderTarget, target.ID)
		}
		reasons = append(reasons, safeReviewCode(target.ReasonCode))
	}
	return events, anomalies, orderTarget, reasons
}

func joinReviewIDs(ids []int64) string {
	if len(ids) == 0 {
		return "-"
	}
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, strconv.FormatInt(id, 10))
	}
	return strings.Join(values, ",")
}

func safeReviewCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	runes := []rune(value)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._:-", r) {
			runes[i] = '_'
		}
	}
	return string(runes)
}
