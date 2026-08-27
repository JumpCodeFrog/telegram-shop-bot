package main

import (
	"fmt"
	"io"
)

type commandSet struct {
	init           func() int
	doctor         func() int
	reconcileStars func([]string) int
	paymentReview  func([]string) int
	run            func() int
}

func dispatch(args []string, out io.Writer, commands commandSet, buildVersion, buildCommit, buildDate string) int {
	command := "run"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "run":
		if len(args) > 1 {
			return printUsageError(out, "run does not accept arguments")
		}
		return commands.run()
	case "init":
		if len(args) > 1 {
			return printUsageError(out, "init does not accept arguments")
		}
		return commands.init()
	case "doctor":
		if len(args) > 1 {
			return printUsageError(out, "doctor does not accept arguments")
		}
		return commands.doctor()
	case "reconcile-stars":
		return commands.reconcileStars(args[1:])
	case "payment-review":
		return commands.paymentReview(args[1:])
	case "quickstart":
		if len(args) > 1 {
			return printUsageError(out, "quickstart does not accept arguments")
		}
		if code := commands.init(); code != 0 {
			return code
		}
		if code := commands.doctor(); code != 0 {
			return code
		}
		return commands.run()
	case "version", "--version", "-v":
		fmt.Fprintf(out, "telegram-shop-bot %s (commit=%s built=%s)\n", buildVersion, buildCommit, buildDate)
		return 0
	case "help", "--help", "-h":
		printUsage(out)
		return 0
	default:
		return printUsageError(out, fmt.Sprintf("unknown command %q", command))
	}
}

func printUsageError(out io.Writer, message string) int {
	fmt.Fprintf(out, "Error: %s\n\n", message)
	printUsage(out)
	return 2
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: telegram-shop-bot [command] [options]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  quickstart  Configure, verify, and start the shop")
	fmt.Fprintln(out, "  init        Create a private .env with two guided answers")
	fmt.Fprintln(out, "  doctor      Check configuration, SQLite, Redis, and Telegram")
	fmt.Fprintln(out, "  reconcile-stars  Compare the local ledger with Telegram Stars")
	fmt.Fprintln(out, "  payment-review   List, preview, and resolve quarantined payment facts")
	fmt.Fprintln(out, "  run         Start the shop (default when no command is given)")
	fmt.Fprintln(out, "  version     Print build information")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run 'telegram-shop-bot payment-review help' for the guarded review workflow.")
}
