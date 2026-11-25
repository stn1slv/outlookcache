// Package cli is the command-line interface of outlookcache.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stn1slv/outlookcache/internal/service"
)

// ErrInvalidFlag reports a flag value the command does not accept.
var ErrInvalidFlag = errors.New("invalid flag")

// Environment variables.
const (
	envStore      = "OUTLOOKCACHE_STORE"
	envMaxContent = "OUTLOOKCACHE_MAX_CONTENT"
)

type globals struct {
	store      string
	account    string
	maxContent int
	verbose    bool
	ctx        context.Context
	stdout     io.Writer
	stderr     io.Writer
}

func (g *globals) logger() *slog.Logger {
	level := slog.LevelWarn
	if g.verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(g.stderr, &slog.HandlerOptions{Level: level}))
}

func (g *globals) options() service.Options {
	return service.Options{MaxContent: g.maxContent, Now: time.Now}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// NewRootCommand returns the outlookcache command with its subcommands.
func NewRootCommand(version string, stdout, stderr io.Writer) *cobra.Command {
	g := &globals{stdout: stdout, stderr: stderr}
	root := &cobra.Command{
		Use:   "outlookcache",
		Short: "Read the local mail cache of new Outlook for Mac",
		Long: "outlookcache reads the mail cache (HxStore.hxd) that new Outlook for Mac keeps on this computer.\n" +
			"It needs no Microsoft Graph access, no sign-in and no network. All commands are read-only and print JSON.\n" +
			"The store is copied to a private temporary folder first; the original file is never changed.\n\n" +
			"Messages marked \"confirmed\": false may be deleted: the reader cannot yet tell all deleted messages\n" +
			"from live ones (see docs/STORAGE.md). Use --confirmed-only to hide them.\n\n" +
			"The store of the default profile is found automatically. Use --store or " + envStore + " to choose another file.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		g.ctx = cmd.Context()
		return validate(cmd)
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	flags := root.PersistentFlags()
	flags.StringVar(&g.store, "store", os.Getenv(envStore), "path to HxStore.hxd (default: the default profile; env "+envStore+")")
	flags.StringVar(&g.account, "account", "", "only this account, by ID or e-mail address (see the accounts command)")
	flags.IntVar(&g.maxContent, "max-content", envInt(envMaxContent, 4000), "characters of body text in read and thread, 0 for no limit (env "+envMaxContent+")")
	flags.BoolVarP(&g.verbose, "verbose", "v", false, "log details, such as skipped records, to stderr")

	root.AddCommand(
		accountsCommand(g),
		foldersCommand(g),
		messagesCommand(g),
		searchCommand(g),
		readCommand(g),
		threadCommand(g),
	)
	return root
}

// validate rejects negative numeric flags.
func validate(cmd *cobra.Command) error {
	for _, name := range []string{"max-content", "limit", "days"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if strings.HasPrefix(f.Value.String(), "-") {
			return fmt.Errorf("%w: --%s must be 0 or more, got %s", ErrInvalidFlag, name, f.Value.String())
		}
	}
	return nil
}

func encodeJSON(w io.Writer, out any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// writeJSON prints out and the summary line. unconfirmed < 0 leaves the
// unconfirmed count out.
func writeJSON(g *globals, snap *service.Snapshot, count, unconfirmed int, out any) error {
	if err := encodeJSON(g.stdout, out); err != nil {
		return err
	}
	line := fmt.Sprintf("# %d result(s), %d record(s) skipped, %d damaged section(s)", count, snap.Skipped(), snap.Damaged())
	if unconfirmed >= 0 {
		line += fmt.Sprintf(", %d unconfirmed", unconfirmed)
	}
	if _, err := fmt.Fprintln(g.stderr, line); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func (g *globals) load() (*service.Snapshot, error) {
	return service.Load(g.ctx, g.store, g.logger())
}

func messageFilterFlags(cmd *cobra.Command, f *service.Filter, defLimit, defDays int) {
	cmd.Flags().StringVar(&f.Folder, "folder", "", "only this folder, by ID, role (inbox, sent, drafts, deleted, junk, archive, outbox) or name")
	cmd.Flags().BoolVar(&f.Unread, "unread", false, "only unread messages")
	cmd.Flags().IntVar(&f.Days, "days", defDays, "only messages of the last N days, 0 for all")
	cmd.Flags().IntVar(&f.Limit, "limit", defLimit, limitHelp)
	cmd.Flags().BoolVar(&f.ConfirmedOnly, "confirmed-only", false, "hide messages that may be deleted (confirmed: false)")
}

const limitHelp = "maximum number of results, 0 for no limit"

func unconfirmed(rows []service.MessageRow) int {
	n := 0
	for _, r := range rows {
		if !r.Confirmed {
			n++
		}
	}
	return n
}

func accountsCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "List the mail accounts with their standard folders",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			out, err := snap.Accounts(g.account)
			if err != nil {
				return err
			}
			return writeJSON(g, snap, len(out), -1, out)
		},
	}
}

func foldersCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "folders",
		Short: "List mail folders with total and unread message counts",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			out, err := snap.Folders(g.ctx, g.account)
			if err != nil {
				return err
			}
			return writeJSON(g, snap, len(out), -1, out)
		},
	}
}

func messagesCommand(g *globals) *cobra.Command {
	var f service.Filter
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List messages without bodies, newest first",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			f.Account = g.account
			out, err := snap.Messages(g.ctx, f, g.options())
			if err != nil {
				return err
			}
			return writeJSON(g, snap, len(out), unconfirmed(out), out)
		},
	}
	messageFilterFlags(cmd, &f, 50, 0)
	return cmd
}

func searchCommand(g *globals) *cobra.Command {
	var f service.Filter
	var inBody bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Find messages whose subject, preview, sender, recipients or attachment names contain QUERY, ignoring case",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			f.Account = g.account
			out, err := snap.Search(g.ctx, args[0], inBody, f, g.options())
			if err != nil {
				return err
			}
			return writeJSON(g, snap, len(out), unconfirmed(out), out)
		},
	}
	messageFilterFlags(cmd, &f, 50, 0)
	cmd.Flags().BoolVar(&inBody, "body", false, "also search the body text (reads every body, slower)")
	return cmd
}

func readCommand(g *globals) *cobra.Command {
	var quoted bool
	cmd := &cobra.Command{
		Use:   "read MESSAGE_ID",
		Short: "Show one message with its body as text",
		Long: "Show one message with its body as text.\n" +
			"Quoted and forwarded text is cut off unless --quoted is set.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			out, err := snap.Message(g.ctx, args[0], quoted, g.options())
			if err != nil {
				return err
			}
			n := 0
			if !out.Confirmed {
				n = 1
			}
			return writeJSON(g, snap, 1, n, out)
		},
	}
	cmd.Flags().BoolVar(&quoted, "quoted", false, "keep quoted and forwarded text")
	return cmd
}

func threadCommand(g *globals) *cobra.Command {
	var quoted bool
	var limit int
	cmd := &cobra.Command{
		Use:   "thread CONVERSATION_ID",
		Short: "Show the messages of a conversation with their bodies, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			snap, err := g.load()
			if err != nil {
				return err
			}
			out, err := snap.Thread(g.ctx, args[0], quoted, limit, g.options())
			if err != nil {
				return err
			}
			n := 0
			for _, m := range out {
				if !m.Confirmed {
					n++
				}
			}
			return writeJSON(g, snap, len(out), n, out)
		},
	}
	cmd.Flags().BoolVar(&quoted, "quoted", false, "keep quoted and forwarded text")
	cmd.Flags().IntVar(&limit, "limit", 20, limitHelp+" (the newest messages are kept)")
	return cmd
}

// Execute runs the command line and returns the exit code.
func Execute(ctx context.Context, version string, args []string) int {
	root := NewRootCommand(version, os.Stdout, os.Stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
