// Package cli is the command-line interface of outlookcache.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/stn1slv/outlookcache/internal/mcpserver"
	"github.com/stn1slv/outlookcache/internal/service"
)

// ErrInvalidFlag reports a flag value the command does not accept.
var ErrInvalidFlag = errors.New("invalid flag")

// Environment variables.
const (
	envStore      = "OUTLOOKCACHE_STORE"
	envMaxContent = "OUTLOOKCACHE_MAX_CONTENT"
	envCacheTTL   = "OUTLOOKCACHE_CACHE_TTL"
)

// envError is a malformed environment variable. It is reported only by
// commands that have its flag, and only when that flag is not set.
type envError struct {
	flag string
	err  error
}

type globals struct {
	envErrs    []envError
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

// envInt reads a whole number from min to max from the environment
// variable name, the default of flag; an unset variable gives def. A
// malformed value is kept and reported before a command with that flag
// runs, unless the flag is set.
func (g *globals) envInt(name, flag string, def, minV, maxV int) int {
	raw, set := os.LookupEnv(name)
	if !set || strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < minV || v > maxV {
		g.envErrs = append(g.envErrs, envError{flag, fmt.Errorf("%w: %s must be a whole number from %d to %d", ErrInvalidFlag, name, minV, maxV)})
		return def
	}
	return v
}

// checkEnv returns the errors of the environment variables that apply to
// cmd.
func (g *globals) checkEnv(cmd *cobra.Command) error {
	var errs []error
	for _, e := range g.envErrs {
		if cmd.Flags().Lookup(e.flag) != nil && !cmd.Flags().Changed(e.flag) {
			errs = append(errs, e.err)
		}
	}
	return errors.Join(errs...)
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
		if err := g.checkEnv(cmd); err != nil {
			return err
		}
		return validate(cmd)
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	flags := root.PersistentFlags()
	flags.StringVar(&g.store, "store", "", "path to HxStore.hxd (default: env "+envStore+", else the default profile)")
	flags.StringVar(&g.account, "account", "", "only this account, by ID or e-mail address (see the accounts command)")
	flags.IntVar(&g.maxContent, "max-content", g.envInt(envMaxContent, "max-content", 4000, 0, math.MaxInt), "characters of body text in read and thread, 0 for no limit (env "+envMaxContent+")")
	flags.BoolVarP(&g.verbose, "verbose", "v", false, "log load statistics to stderr")

	root.AddCommand(
		accountsCommand(g),
		foldersCommand(g),
		messagesCommand(g),
		searchCommand(g),
		readCommand(g),
		threadCommand(g),
		mcpCommand(g, version),
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

// summary is the content of the stderr summary line.
type summary struct {
	count       int
	more        bool // the limit cut off further results
	unconfirmed int  // shown when withUnconfirmed
	unreadable  int  // bodies a body search could not read
	withUnconf  bool
}

// writeJSON prints out and the summary line, for example
// "# 50 result(s) (limit reached, more exist), 0 record(s) skipped,
// 0 damaged section(s), 131 undecoded block(s), 12 unconfirmed".
func writeJSON(g *globals, snap *service.Snapshot, sum summary, out any) error {
	if err := encodeJSON(g.stdout, out); err != nil {
		return err
	}
	line := fmt.Sprintf("# %d result(s)", sum.count)
	if sum.more {
		line += " (limit reached, more exist)"
	}
	line += fmt.Sprintf(", %d record(s) skipped, %d damaged section(s), %d undecoded block(s)", snap.Skipped(), snap.Damaged(), snap.Undecoded())
	if sum.withUnconf {
		line += fmt.Sprintf(", %d unconfirmed", sum.unconfirmed)
	}
	if sum.unreadable > 0 {
		line += fmt.Sprintf(", %d body(ies) could not be searched", sum.unreadable)
	}
	if _, err := fmt.Fprintln(g.stderr, line); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

// storePath is --store, else the environment, else "" for the default.
func (g *globals) storePath() string {
	if g.store != "" {
		return g.store
	}
	return os.Getenv(envStore)
}

func (g *globals) load() (*service.Snapshot, error) {
	return service.Load(g.ctx, g.storePath(), g.logger())
}

func listSummary(l service.List) summary {
	return summary{count: len(l.Messages), more: l.More, unconfirmed: l.Unconfirmed(), unreadable: l.UnreadableBodies, withUnconf: true}
}

func messageFilterFlags(cmd *cobra.Command, f *service.Filter) {
	cmd.Flags().StringVar(&f.Folder, "folder", "", "only this folder: an ID, else a role (inbox, sent, drafts, deleted, junk, archive, outbox), else a name")
	cmd.Flags().BoolVar(&f.Unread, "unread", false, "only unread messages")
	cmd.Flags().IntVar(&f.Days, "days", 0, "only messages of the last N days, 0 for all")
	cmd.Flags().IntVar(&f.Limit, "limit", 50, limitHelp)
	cmd.Flags().BoolVar(&f.ConfirmedOnly, "confirmed-only", false, "hide messages that may be deleted (confirmed: false)")
}

const limitHelp = "maximum number of results, 0 for no limit"

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
			return writeJSON(g, snap, summary{count: len(out)}, out)
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
			sum := summary{count: len(out), withUnconf: true}
			for _, f := range out {
				sum.unconfirmed += f.Unconfirmed
			}
			return writeJSON(g, snap, sum, out)
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
			return writeJSON(g, snap, listSummary(out), out.Messages)
		},
	}
	messageFilterFlags(cmd, &f)
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
			return writeJSON(g, snap, listSummary(out), out.Messages)
		},
	}
	messageFilterFlags(cmd, &f)
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
			out, err := snap.Message(g.ctx, args[0], g.account, quoted, g.options())
			if err != nil {
				return err
			}
			sum := summary{count: 1, withUnconf: true}
			if !out.Confirmed {
				sum.unconfirmed = 1
			}
			return writeJSON(g, snap, sum, out)
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
			out, more, err := snap.Thread(g.ctx, args[0], g.account, quoted, limit, g.options())
			if err != nil {
				return err
			}
			sum := summary{count: len(out), more: more, withUnconf: true}
			for _, m := range out {
				if !m.Confirmed {
					sum.unconfirmed++
				}
			}
			return writeJSON(g, snap, sum, out)
		},
	}
	cmd.Flags().BoolVar(&quoted, "quoted", false, "keep quoted and forwarded text")
	cmd.Flags().IntVar(&limit, "limit", 20, limitHelp+" (the newest messages are kept)")
	return cmd
}

func mcpCommand(g *globals, version string) *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the read-only MCP server on stdin and stdout",
		Long: "Run the read-only MCP server on stdin and stdout.\n" +
			"The store is decoded on the first tool call. Later calls reuse it until the TTL ends or Outlook changes the file.\n" +
			"With --account, every tool serves only that account.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ttl <= 0 {
				return fmt.Errorf("%w: --cache-ttl must be more than 0", ErrInvalidFlag)
			}
			cache := service.NewCache(g.storePath(), ttl, g.logger())
			server := mcpserver.New(cache, g.options(), version, g.account)
			transport := &mcp.IOTransport{Reader: io.NopCloser(cmd.InOrStdin()), Writer: nopWriteCloser{g.stdout}}
			return stopIsClean(cmd.Context(), server.Run(cmd.Context(), transport))
		},
	}
	defTTL := time.Duration(g.envInt(envCacheTTL, "cache-ttl", 180, 1, maxTTLSeconds)) * time.Second
	cmd.Flags().DurationVar(&ttl, "cache-ttl", defTTL, "how long a decoded store is reused, for example 3m (env "+envCacheTTL+", in seconds)")
	return cmd
}

// maxTTLSeconds is the largest OUTLOOKCACHE_CACHE_TTL, one year.
const maxTTLSeconds = 365 * 24 * 60 * 60

// stopIsClean drops the error of a server stopped by SIGINT or SIGTERM.
func stopIsClean(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil //nolint:nilerr // a stop signal is a clean exit, not an error
	}
	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

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
