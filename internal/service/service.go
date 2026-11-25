// Package service answers questions about the Outlook store: accounts,
// folders, message lists, searches, single messages with their bodies and
// threads. The CLI uses it.
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
	"strconv"
	"strings"
	"time"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/snapshot"
	"github.com/stn1slv/outlookcache/internal/store"
)

var (
	// ErrStoreMissing reports that HxStore.hxd does not exist.
	ErrStoreMissing = errors.New("outlook store not found")
	// ErrStoreAccess reports that HxStore.hxd exists but cannot be read.
	ErrStoreAccess = errors.New("outlook store cannot be read")
	// ErrNotFound reports an unknown account, folder, message or
	// conversation.
	ErrNotFound = errors.New("not found")
	// ErrInvalidArgument reports an argument the service does not accept.
	ErrInvalidArgument = errors.New("invalid argument")
)

// accessHint explains the usual cause of a permission error on macOS.
const accessHint = "the terminal app may need Full Disk Access, or access to the Outlook data folder, in System Settings > Privacy & Security"

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
	// Now returns the current time (for Filter.Days); nil means time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

func (o Options) validate() error {
	if o.MaxContent < 0 {
		return fmt.Errorf("%w: max content must be 0 or more", ErrInvalidArgument)
	}
	return nil
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
// and decodes it. The original file is only opened for reading. A nil
// logger logs nothing.
func Load(ctx context.Context, path string, logger *slog.Logger) (*Snapshot, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if path == "" {
		p, err := DefaultStorePath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		return nil, storeError(path, err)
	}
	data, err := copyAndRead(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
			return nil, storeError(path, err)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := store.Load(data, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	st := s.Stats
	logger.Debug("store loaded", "messages", len(s.Messages), "folders", len(s.Folders), "accounts", len(s.Accounts),
		"skipped_records", st.SkippedRecords, "damaged_sections", len(st.Damage), "unindexed_damaged", st.UnindexedDamaged,
		"undecoded_blocks", st.UndecodedBlocks, "unconfirmed", st.Unconfirmed, "ambiguous", st.Ambiguous,
		"index_unreadable", st.IndexUnreadable, "index_areas_differ", st.IndexAreasDiffer)
	if st.IndexUnreadable {
		logger.Warn("the block index has an unexpected layout; every message is marked unconfirmed")
	}
	snap := &Snapshot{Store: s, Profile: filepath.Dir(path), folders: map[string]store.Folder{}}
	for _, f := range s.Folders {
		snap.folders[f.ID] = f
	}
	return snap, nil
}

func storeError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w at %s (is new Outlook for Mac installed and signed in?)", ErrStoreMissing, path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: %w (%s)", ErrStoreAccess, err, accessHint)
	default:
		return fmt.Errorf("%w: %w", ErrStoreAccess, err)
	}
}

func copyAndRead(ctx context.Context, path string) (data []byte, err error) {
	tmp, err := snapshot.New()
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := tmp.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary copy: %w", cerr))
		}
	}()
	cp, err := tmp.Copy(ctx, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(cp)
}

// Skipped is the number of records the reader could not decode.
func (s *Snapshot) Skipped() int { return s.Store.Stats.SkippedRecords }

// Damaged is the number of damaged sections that the index points at.
func (s *Snapshot) Damaged() int { return len(s.Store.Stats.Damage) }

// Undecoded is the number of blocks in a format the reader cannot read
// (type 16, see docs/STORAGE.md).
func (s *Snapshot) Undecoded() int { return s.Store.Stats.UndecodedBlocks }

// MessageRow is a message with the name and role of its folder.
type MessageRow struct {
	store.Message
	FolderName string `json:"folder_name,omitempty"`
	FolderRole string `json:"folder_role,omitempty"`
}

// FolderRow is a folder with message counts. Unconfirmed messages are
// included in Total and Unread and also counted on their own.
type FolderRow struct {
	store.Folder
	Total       int `json:"total"`
	Unread      int `json:"unread"`
	Unconfirmed int `json:"unconfirmed"`
}

// Filter selects messages. Empty fields select everything.
type Filter struct {
	Account       string // account ID or address
	Folder        string // folder ID, else role (inbox, sent, ...), else name; ignoring case
	Unread        bool   // only unread messages
	Days          int    // only messages received in the last N days
	Limit         int    // maximum number of results, 0 for no limit
	ConfirmedOnly bool   // only messages whose copy is in a direct index slot
}

func (f Filter) validate() error {
	if f.Days < 0 || f.Limit < 0 {
		return fmt.Errorf("%w: days and limit must be 0 or more", ErrInvalidArgument)
	}
	return nil
}

// List is a list of messages.
type List struct {
	Messages []MessageRow
	// More is true when the limit cut off further matches.
	More bool
	// UnreadableBodies counts bodies a body search could not read; those
	// messages may match but are not listed.
	UnreadableBodies int
}

// Unconfirmed counts the listed messages that may be deleted.
func (l List) Unconfirmed() int {
	n := 0
	for _, m := range l.Messages {
		if !m.Confirmed {
			n++
		}
	}
	return n
}

// Accounts returns the accounts, or the one that account names.
func (s *Snapshot) Accounts(account string) ([]store.Account, error) {
	id, err := s.AccountID(account)
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

// AccountID resolves an account ID or address; "" stays "". The error
// does not repeat the value, so an address is never printed.
func (s *Snapshot) AccountID(account string) (string, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return "", nil
	}
	want := strings.ToLower(account)
	for _, a := range s.Store.Accounts {
		if a.ID == want || a.Address != "" && strings.EqualFold(a.Address, account) {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("%w: no account with this ID or address (list the accounts to see the valid values)", ErrNotFound)
}

// Folders returns the folders of all accounts or of one, with message
// counts.
func (s *Snapshot) Folders(ctx context.Context, account string) ([]FolderRow, error) {
	id, err := s.AccountID(account)
	if err != nil {
		return nil, err
	}
	total, unread, unconfirmed := map[string]int{}, map[string]int{}, map[string]int{}
	for _, m := range s.Store.Messages {
		if id != "" && m.AccountID != id {
			continue
		}
		total[m.FolderID]++
		if !m.Read {
			unread[m.FolderID]++
		}
		if !m.Confirmed {
			unconfirmed[m.FolderID]++
		}
	}
	out := []FolderRow{}
	for _, f := range s.Store.Folders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id == "" || f.AccountID == id {
			out = append(out, FolderRow{Folder: f, Total: total[f.ID], Unread: unread[f.ID], Unconfirmed: unconfirmed[f.ID]})
		}
	}
	return out, nil
}

// folderIDs resolves a folder of the account (all accounts when accountID
// is ""): a folder ID wins over a role, and a role over a name. The error
// does not repeat the value.
func (s *Snapshot) folderIDs(folder, accountID string) (map[string]bool, error) {
	if folder == "" {
		return nil, nil
	}
	want := strings.ToLower(folder)
	byID, byRole, byName := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range s.Store.Folders {
		if accountID != "" && f.AccountID != accountID {
			continue
		}
		switch {
		case f.ID == want:
			byID[f.ID] = true
		case f.Role != "" && f.Role == want:
			byRole[f.ID] = true
		case strings.EqualFold(f.Name, folder):
			byName[f.ID] = true
		}
	}
	for _, set := range []map[string]bool{byID, byRole, byName} {
		if len(set) > 0 {
			return set, nil
		}
	}
	return nil, fmt.Errorf("%w: no folder with this ID, role or name (list the folders to see the valid values)", ErrNotFound)
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

// newer orders messages newest first; equal times by ID, as numbers.
func newer(a, b store.Message) bool {
	ta, tb := when(a), when(b)
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	if len(a.ID) != len(b.ID) {
		return len(a.ID) > len(b.ID)
	}
	return a.ID > b.ID
}

// matcher decides whether a message matches; unreadable is true when the
// message could not be checked.
type matcher func(store.Message) (ok, unreadable bool)

// selectMessages applies the filter, sorts newest first and then runs
// match in that order until the limit is reached, so a costly match runs
// only as often as needed.
func (s *Snapshot) selectMessages(ctx context.Context, f Filter, opts Options, match matcher) (List, error) {
	if err := f.validate(); err != nil {
		return List{}, err
	}
	accountID, err := s.AccountID(f.Account)
	if err != nil {
		return List{}, err
	}
	folders, err := s.folderIDs(f.Folder, accountID)
	if err != nil {
		return List{}, err
	}
	var since time.Time
	if f.Days > 0 {
		since = opts.now().AddDate(0, 0, -f.Days)
	}
	var candidates []store.Message
	for _, m := range s.Store.Messages {
		switch {
		case accountID != "" && m.AccountID != accountID,
			folders != nil && !folders[m.FolderID],
			f.Unread && m.Read,
			f.ConfirmedOnly && !m.Confirmed,
			f.Days > 0 && when(m).Before(since):
			continue
		}
		candidates = append(candidates, m)
	}
	sort.Slice(candidates, func(i, j int) bool { return newer(candidates[i], candidates[j]) })
	out := List{Messages: []MessageRow{}}
	for _, m := range candidates {
		if err := ctx.Err(); err != nil {
			return List{}, err
		}
		if match != nil {
			ok, unreadable := match(m)
			if unreadable {
				out.UnreadableBodies++
			}
			if !ok {
				continue
			}
		}
		if f.Limit > 0 && len(out.Messages) == f.Limit {
			out.More = true
			break
		}
		out.Messages = append(out.Messages, s.row(m))
	}
	return out, nil
}

// Messages returns the messages that the filter selects, newest first.
func (s *Snapshot) Messages(ctx context.Context, f Filter, opts Options) (List, error) {
	return s.selectMessages(ctx, f, opts, nil)
}

// Search returns messages whose subject, preview, sender, recipients or
// attachment names contain query, ignoring case. With inBody it also
// searches the body text (quoted text included), which reads bodies and
// is slower; bodies that cannot be read are counted in the result.
func (s *Snapshot) Search(ctx context.Context, query string, inBody bool, f Filter, opts Options) (List, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return List{}, fmt.Errorf("%w: empty search text", ErrInvalidArgument)
	}
	return s.selectMessages(ctx, f, opts, func(m store.Message) (bool, bool) {
		if strings.Contains(strings.ToLower(headerText(m)), q) {
			return true, false
		}
		if !inBody {
			return false, false
		}
		b := s.body(m, true, 0)
		if b.Error != "" || b.Kind == store.BodyUnparsed {
			return false, true
		}
		return strings.Contains(strings.ToLower(b.Text), q), false
	})
}

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
	b.Text, b.Truncated = truncate(b.Text, maxContent)
	return b
}

// truncate cuts s after max characters; max 0 means no limit.
func truncate(s string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return s, false
	}
	n := 0
	for i := range s {
		if n == maxChars {
			return s[:i], true
		}
		n++
	}
	return s, false
}

// canonID returns the store form of a hexadecimal ID (lower case, no
// leading zeros, an optional 0x prefix removed).
func canonID(id string) (string, bool) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "0x"), 16, 64)
	if err != nil || v == 0 {
		return "", false
	}
	return strconv.FormatUint(v, 16), true
}

// Message returns one message with its body. With account, the message
// must belong to that account. With quoted, quoted and forwarded text is
// kept.
func (s *Snapshot) Message(ctx context.Context, id, account string, quoted bool, opts Options) (MessageDetail, error) {
	if err := ctx.Err(); err != nil {
		return MessageDetail{}, err
	}
	if err := opts.validate(); err != nil {
		return MessageDetail{}, err
	}
	accountID, err := s.AccountID(account)
	if err != nil {
		return MessageDetail{}, err
	}
	want, ok := canonID(id)
	if ok {
		for _, m := range s.Store.Messages {
			if m.ID == want && (accountID == "" || m.AccountID == accountID) {
				return MessageDetail{MessageRow: s.row(m), Body: s.body(m, quoted, opts.MaxContent)}, nil
			}
		}
	}
	return MessageDetail{}, fmt.Errorf("%w: message %q", ErrNotFound, id)
}

// Thread returns the messages of a conversation with their bodies, oldest
// first; with limit > 0 only the newest limit messages, and more is true
// when older ones were left out. With account, only that account's
// messages are included.
func (s *Snapshot) Thread(ctx context.Context, conversationID, account string, quoted bool, limit int, opts Options) (out []MessageDetail, more bool, err error) {
	if err := opts.validate(); err != nil {
		return nil, false, err
	}
	id, ok := canonID(conversationID)
	if !ok {
		return nil, false, fmt.Errorf("%w: conversation %q", ErrNotFound, conversationID)
	}
	list, err := s.selectMessages(ctx, Filter{Account: account, Limit: limit}, opts, func(m store.Message) (bool, bool) {
		return m.ConversationID == id, false
	})
	if err != nil {
		return nil, false, err
	}
	if len(list.Messages) == 0 {
		return nil, false, fmt.Errorf("%w: conversation %q", ErrNotFound, conversationID)
	}
	out = make([]MessageDetail, 0, len(list.Messages))
	for i := len(list.Messages) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		row := list.Messages[i]
		out = append(out, MessageDetail{MessageRow: row, Body: s.body(row.Message, quoted, opts.MaxContent)})
	}
	return out, list.More, nil
}
