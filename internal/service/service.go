// Package service answers questions about the Outlook store: accounts,
// folders, message lists, searches, single messages with their bodies and
// threads. The CLI and the MCP server both use it.
package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/snapshot"
	"github.com/stn1slv/outlookcache/internal/store"
)

var (
	// ErrStoreMissing reports that HxStore.hxd was not found or cannot be
	// read.
	ErrStoreMissing = errors.New("outlook store not found")
	// ErrNotFound reports an unknown account, folder, message or
	// conversation.
	ErrNotFound = errors.New("not found")
)

// storeRel is the store path of the default profile, relative to the home
// folder (new Outlook for Mac).
const storeRel = "Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles/Main Profile/HxStore.hxd"

// DefaultStorePath returns the HxStore.hxd path of the default profile.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrStoreMissing, err)
	}
	return filepath.Join(home, storeRel), nil
}

// Options control the content of results.
type Options struct {
	// MaxContent is the maximum number of characters of body text; 0
	// means no limit.
	MaxContent int
	// Now returns the current time (for --days); nil means time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// Snapshot is the decoded content of one copy of the store.
type Snapshot struct {
	Store *store.Store
	// Profile is the folder that holds HxStore.hxd; EFMData body files
	// are read, read-only, below it.
	Profile string
	folders map[string]store.Folder
}

// Load copies the store at path (the default profile when path is "") into
// a private temporary folder, reads the copy into memory, removes the copy
// and decodes it. The original file is only opened for reading.
func Load(ctx context.Context, path string, logger *slog.Logger) (*Snapshot, error) {
	if path == "" {
		p, err := DefaultStorePath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s (is new Outlook for Mac installed? use --store to choose a file)", ErrStoreMissing, path)
		}
		return nil, fmt.Errorf("%w: %w (the terminal may need access to the Outlook data folder)", ErrStoreMissing, err)
	}
	data, err := copyAndRead(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := store.Load(data, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	logger.Debug("store loaded", "messages", len(s.Messages), "folders", len(s.Folders), "accounts", len(s.Accounts),
		"skipped_records", s.Stats.SkippedRecords, "damaged_sections", len(s.Stats.Damage),
		"unconfirmed", s.Stats.Unconfirmed, "undecoded_blocks", s.Stats.UndecodedBlocks)
	snap := &Snapshot{Store: s, Profile: filepath.Dir(path), folders: map[string]store.Folder{}}
	for _, f := range s.Folders {
		snap.folders[f.ID] = f
	}
	return snap, nil
}

func copyAndRead(ctx context.Context, path string) (data []byte, err error) {
	snap, err := snapshot.New()
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := snap.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("remove snapshot: %w", cerr)
		}
	}()
	cp, err := snap.Copy(ctx, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(cp)
}

// Skipped is the number of records the reader could not decode.
func (s *Snapshot) Skipped() int { return s.Store.Stats.SkippedRecords }

// Damaged is the number of damaged sections that the index points at.
func (s *Snapshot) Damaged() int { return len(s.Store.Stats.Damage) }

// MessageRow is a message with the name and role of its folder.
type MessageRow struct {
	store.Message
	FolderName string `json:"folder_name,omitempty"`
	FolderRole string `json:"folder_role,omitempty"`
}

// FolderRow is a folder with message counts.
type FolderRow struct {
	store.Folder
	Total  int `json:"total"`
	Unread int `json:"unread"`
}

// Filter selects messages. Empty fields select everything.
type Filter struct {
	Account       string // account ID or address
	Folder        string // folder ID, role (inbox, sent, ...) or name, ignoring case
	Unread        bool   // only unread messages
	Days          int    // only messages received in the last N days
	Limit         int    // maximum number of results, 0 for no limit
	ConfirmedOnly bool   // only messages whose copy is in a direct index slot
}

// Accounts returns the accounts, or the one that account names.
func (s *Snapshot) Accounts(account string) ([]store.Account, error) {
	id, err := s.accountID(account)
	if err != nil {
		return nil, err
	}
	out := []store.Account{}
	for _, a := range s.Store.Accounts {
		if id == "" || a.ID == id {
			out = append(out, a)
		}
	}
	return out, nil
}

// accountID resolves an account ID or address; "" stays "".
func (s *Snapshot) accountID(account string) (string, error) {
	if account == "" {
		return "", nil
	}
	for _, a := range s.Store.Accounts {
		if a.ID == strings.ToLower(account) || a.Address != "" && strings.EqualFold(a.Address, account) {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("%w: account %q", ErrNotFound, account)
}

// Folders returns the folders of all accounts or of one, with message
// counts.
func (s *Snapshot) Folders(ctx context.Context, account string) ([]FolderRow, error) {
	id, err := s.accountID(account)
	if err != nil {
		return nil, err
	}
	total, unread := map[string]int{}, map[string]int{}
	for _, m := range s.Store.Messages {
		total[m.FolderID]++
		if !m.Read {
			unread[m.FolderID]++
		}
	}
	out := []FolderRow{}
	for _, f := range s.Store.Folders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id == "" || f.AccountID == id {
			out = append(out, FolderRow{Folder: f, Total: total[f.ID], Unread: unread[f.ID]})
		}
	}
	return out, nil
}

// folderIDs resolves a folder ID, role or name to the matching folder IDs
// of the account (all accounts when accountID is "").
func (s *Snapshot) folderIDs(folder, accountID string) (map[string]bool, error) {
	if folder == "" {
		return nil, nil
	}
	want := strings.ToLower(folder)
	out := map[string]bool{}
	for _, f := range s.Store.Folders {
		if accountID != "" && f.AccountID != accountID {
			continue
		}
		if f.ID == want || f.Role == want || strings.EqualFold(f.Name, folder) {
			out[f.ID] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: folder %q", ErrNotFound, folder)
	}
	return out, nil
}

func (s *Snapshot) row(m store.Message) MessageRow {
	f := s.folders[m.FolderID]
	return MessageRow{Message: m, FolderName: f.Name, FolderRole: f.Role}
}

// when returns the received time, else the sent time, else zero.
func when(m store.Message) time.Time {
	switch {
	case m.Received != nil:
		return *m.Received
	case m.Sent != nil:
		return *m.Sent
	}
	return time.Time{}
}

// selectMessages applies the filter and match, newest first.
func (s *Snapshot) selectMessages(ctx context.Context, f Filter, opts Options, match func(store.Message) (bool, error)) ([]MessageRow, error) {
	accountID, err := s.accountID(f.Account)
	if err != nil {
		return nil, err
	}
	folders, err := s.folderIDs(f.Folder, accountID)
	if err != nil {
		return nil, err
	}
	var since time.Time
	if f.Days > 0 {
		since = opts.now().Add(-time.Duration(f.Days) * 24 * time.Hour)
	}
	var picked []store.Message
	for _, m := range s.Store.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case accountID != "" && m.AccountID != accountID,
			folders != nil && !folders[m.FolderID],
			f.Unread && m.Read,
			f.ConfirmedOnly && !m.Confirmed,
			f.Days > 0 && when(m).Before(since):
			continue
		}
		if match != nil {
			ok, err := match(m)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		picked = append(picked, m)
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := when(picked[i]), when(picked[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return picked[i].ID > picked[j].ID
	})
	if f.Limit > 0 && len(picked) > f.Limit {
		picked = picked[:f.Limit]
	}
	out := make([]MessageRow, 0, len(picked))
	for _, m := range picked {
		out = append(out, s.row(m))
	}
	return out, nil
}

// Messages returns the messages that the filter selects, newest first.
func (s *Snapshot) Messages(ctx context.Context, f Filter, opts Options) ([]MessageRow, error) {
	return s.selectMessages(ctx, f, opts, nil)
}

// Search returns messages whose subject, preview, sender, recipients or
// attachment names contain query, ignoring case. With inBody it also
// searches the body text, which reads every body and is slower.
func (s *Snapshot) Search(ctx context.Context, query string, inBody bool, f Filter, opts Options) ([]MessageRow, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("%w: empty search text", ErrInvalidArgument)
	}
	return s.selectMessages(ctx, f, opts, func(m store.Message) (bool, error) {
		if strings.Contains(strings.ToLower(headerText(m)), q) {
			return true, nil
		}
		if !inBody {
			return false, nil
		}
		b := s.body(m, true, 0)
		return strings.Contains(strings.ToLower(b.Text), q), nil
	})
}

// ErrInvalidArgument reports an argument the service does not accept.
var ErrInvalidArgument = errors.New("invalid argument")

func headerText(m store.Message) string {
	parts := []string{m.Subject, m.Preview}
	if m.From != nil {
		parts = append(parts, m.From.Name, m.From.Address)
	}
	for _, list := range [][]store.Recipient{m.To, m.Cc, m.Bcc} {
		for _, r := range list {
			parts = append(parts, r.Name, r.Address)
		}
	}
	for _, a := range m.Attachments {
		parts = append(parts, a.Name)
	}
	return strings.Join(parts, "\n")
}

// Body is the text of a message body.
type Body struct {
	// Kind says where the body is stored: inline, efmdata, none or
	// unparsed.
	Kind string `json:"kind"`
	// Source is "inline" or "efmdata:<file>" when a body was read.
	Source string `json:"source,omitempty"`
	Text   string `json:"text"`
	// QuoteRemoved is true when quoted or forwarded text was cut off.
	QuoteRemoved bool `json:"quote_removed"`
	// Truncated is true when the text was cut at MaxContent characters.
	Truncated bool `json:"truncated"`
	// Error says why a stored body could not be read.
	Error string `json:"error,omitempty"`
}

// MessageDetail is a message with its body.
type MessageDetail struct {
	MessageRow
	Body Body `json:"body"`
}

// body reads and converts the body of m. Without quoted, quoted text is
// cut, unless nothing would be left (a forward without added text).
func (s *Snapshot) body(m store.Message, quoted bool, maxContent int) Body {
	b := Body{Kind: m.BodyKind}
	ref, err := s.Store.Body(m.ID)
	if err != nil {
		b.Error = err.Error()
		return b
	}
	b.Kind = ref.Kind
	if ref.Kind == store.BodyNone || ref.Kind == store.BodyUnparsed {
		return b
	}
	doc, src, err := body.HTML(ref, s.Profile, 0)
	b.Source = src
	if err != nil {
		b.Error = err.Error()
		return b
	}
	t := body.ToText(doc, !quoted)
	if t.QuoteRemoved && t.Text == "" {
		t = body.ToText(doc, false)
	}
	b.Text, b.QuoteRemoved = t.Text, t.QuoteRemoved
	if maxContent > 0 && utf8.RuneCountInString(b.Text) > maxContent {
		b.Text, b.Truncated = string([]rune(b.Text)[:maxContent]), true
	}
	return b
}

// Message returns one message with its body. With quoted, quoted and
// forwarded text is kept.
func (s *Snapshot) Message(ctx context.Context, id string, quoted bool, opts Options) (MessageDetail, error) {
	if err := ctx.Err(); err != nil {
		return MessageDetail{}, err
	}
	for _, m := range s.Store.Messages {
		if m.ID == strings.ToLower(id) {
			return MessageDetail{MessageRow: s.row(m), Body: s.body(m, quoted, opts.MaxContent)}, nil
		}
	}
	return MessageDetail{}, fmt.Errorf("%w: message %q", ErrNotFound, id)
}

// Thread returns the messages of a conversation with their bodies, oldest
// first; with limit > 0 only the newest limit messages.
func (s *Snapshot) Thread(ctx context.Context, conversationID string, quoted bool, limit int, opts Options) ([]MessageDetail, error) {
	id := strings.ToLower(conversationID)
	rows, err := s.selectMessages(ctx, Filter{Limit: limit}, opts, func(m store.Message) (bool, error) {
		return id != "" && m.ConversationID == id, nil
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: conversation %q", ErrNotFound, conversationID)
	}
	out := make([]MessageDetail, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, MessageDetail{MessageRow: rows[i], Body: s.body(rows[i].Message, quoted, opts.MaxContent)})
	}
	return out, nil
}
