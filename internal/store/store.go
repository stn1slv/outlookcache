package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// ErrUnsupportedVersion is returned for store versions other than 'i',
// because every record offset was verified on that version only.
var ErrUnsupportedVersion = errors.New("unsupported HxStore version")

// Account is a mail account found in the store.
type Account struct {
	ID      string            `json:"id"`
	Dir     string            `json:"dir,omitempty"`     // Files/S0/<dir>
	Address string            `json:"address,omitempty"` // from the Sent folder
	Roles   map[string]string `json:"roles"`             // role -> folder ID
}

// Folder is a mail folder with a stored name.
type Folder struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Role      string `json:"role,omitempty"`
}

// Message is one message item with its current copy.
type Message struct {
	ID             string       `json:"id"`
	ConversationID string       `json:"conversation_id,omitempty"`
	ReplyToID      string       `json:"reply_to_id,omitempty"`
	AccountID      string       `json:"account_id,omitempty"`
	FolderID       string       `json:"folder_id"`
	Subject        string       `json:"subject"`
	From           *Recipient   `json:"from,omitempty"`
	To             []Recipient  `json:"to,omitempty"`
	Cc             []Recipient  `json:"cc,omitempty"`
	Bcc            []Recipient  `json:"bcc,omitempty"`
	Sent           *time.Time   `json:"sent,omitempty"`
	Received       *time.Time   `json:"received,omitempty"`
	Read           bool         `json:"read"`
	Importance     string       `json:"importance,omitempty"`
	HasAttachments bool         `json:"has_attachments"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	Preview        string       `json:"preview,omitempty"`
	MessageID      string       `json:"message_id,omitempty"`
	ItemClass      string       `json:"item_class,omitempty"`
	// Confirmed is true when the chosen copy is in a direct slot of the
	// block index. An unconfirmed message is usually live (its index entry
	// sits in an overflow bucket) but may be deleted.
	Confirmed bool `json:"confirmed"`
	// Ambiguous is true when copies with the same stamp disagree in read
	// state or folder and the index does not decide.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Stats reports what the reader saw and skipped.
type Stats struct {
	ValidBlocks     int              `json:"valid_blocks"`
	UndecodedBlocks int              `json:"undecoded_blocks"` // type-16 blocks (format unknown)
	Damage          []hxstore.Damage `json:"damage,omitempty"` // failing blocks that the index points at
	StaleDamaged    int              `json:"stale_damaged"`    // failing blocks the index does not point at
	SkippedRecords  int              `json:"skipped_records"`  // items without a readable string run
	Unconfirmed     int              `json:"unconfirmed"`
	Ambiguous       int              `json:"ambiguous"`
}

// Store is the decoded content of one HxStore.hxd snapshot.
type Store struct {
	Accounts []Account
	Folders  []Folder
	Messages []Message
	Stats    Stats
}

type objVersion struct {
	block  int64
	frame  int
	direct bool
}

// pickVersion prefers a version in a direct index slot, else the last one
// seen in file order.
func pickVersion(vs []objVersion) objVersion {
	for _, v := range vs {
		if v.direct {
			return v
		}
	}
	return vs[len(vs)-1]
}

type rawItem struct {
	itemCopy
	account uint64 // u64 at +32; must not be an ID for items
}

// Load decodes a snapshot of HxStore.hxd. file names the file in damage
// reports.
func Load(data []byte, file string) (*Store, error) {
	h, err := hxstore.ParseHeader(data)
	if err != nil {
		return nil, err
	}
	if !h.Supported() {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedVersion, h.Version)
	}
	ix, err := hxstore.ReadIndex(data)
	if err != nil {
		return nil, err
	}

	// Pass 1: IDs, compact metadata and small records.
	ids := idSpace{}
	var raws []rawItem
	lists := map[uint16]map[uint64][]objVersion{typeTo: {}, typeCc: {}, typeBcc: {}, typeAttachment: {}}
	var recs []folderRec
	roots := map[uint64]rootSeen{}
	dirVotes := map[uint64]map[string]int{} // block key -> dir -> votes
	st, damage, err := hxstore.Scan(data, file, func(b hxstore.Block) error {
		direct := ix.Direct(b.Offset)
		if roles, ok := readRootSlots(b.Payload, b.Key); ok {
			if old, seen := roots[b.Key]; !seen || direct || !old.direct {
				roots[b.Key] = rootSeen{roles, direct}
			}
		}
		for _, d := range filesDirs(b.Payload) {
			if dirVotes[b.Key] == nil {
				dirVotes[b.Key] = map[string]int{}
			}
			dirVotes[b.Key][d]++
		}
		for _, r := range readFolderRecords(b.Payload, idSpace(nil)) {
			r.block, r.direct = b.Offset, direct
			recs = append(recs, r)
		}
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) {
			ids[f.ID] = true
			if m, ok := lists[f.Type]; ok {
				m[f.ID] = append(m[f.ID], objVersion{b.Offset, f.Offset, direct})
				return
			}
			if f.Type != typeMessage || len(f.Data) < itemMinLen {
				return
			}
			var sb [8]byte
			copy(sb[:6], f.Data[itemStampAt:])
			raws = append(raws, rawItem{
				itemCopy: itemCopy{
					id: f.ID, conv: binary.LittleEndian.Uint64(f.Data[itemConvAt:]), folder: binary.LittleEndian.Uint64(f.Data[itemFolderAt:]),
					stamp: binary.LittleEndian.Uint64(sb[:]), status: f.Data[itemStatusAt], block: b.Offset, frame: f.Offset, direct: direct,
				},
				account: binary.LittleEndian.Uint64(f.Data[convAccountAt:]),
			})
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	s := &Store{Stats: Stats{ValidBlocks: st.Valid}}
	for _, d := range damage {
		switch {
		case blockType(data, d.Offset) == 16:
			s.Stats.UndecodedBlocks++
		case ix.Direct(d.Offset):
			s.Stats.Damage = append(s.Stats.Damage, d)
		default:
			s.Stats.StaleDamaged++
		}
	}

	_, folderAccount := s.buildFolders(recs, ids, roots2roles(roots))

	// Choose the current copy of every item.
	copies := map[uint64][]itemCopy{}
	for _, r := range raws {
		if ids.has(r.account) || !ids.has(r.folder) {
			continue // conversation form or another layout
		}
		if !ids.has(r.conv) {
			r.conv = 0
		}
		copies[r.id] = append(copies[r.id], r.itemCopy)
	}
	type chosen struct {
		itemCopy
		confirmed, ambiguous bool
	}
	picks := make(map[uint64]chosen, len(copies))
	need := map[int64]bool{}
	for id, cs := range copies {
		c, conf, amb := pickCurrent(cs)
		picks[id] = chosen{c, conf, amb}
		need[c.block] = true
		for _, t := range []uint16{typeTo, typeCc, typeBcc} {
			if vs := lists[t][id]; len(vs) > 0 {
				need[pickVersion(vs).block] = true
			}
		}
		for _, v := range lists[typeAttachment][id] {
			need[v.block] = true
		}
	}

	// Pass 2: decode only the blocks that hold chosen objects.
	frames := map[int64]map[int][]byte{}
	if _, _, err := hxstore.Scan(data, file, func(b hxstore.Block) error {
		if !need[b.Offset] {
			return nil
		}
		m := map[int][]byte{}
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) { m[f.Offset] = f.Data })
		frames[b.Offset] = m
		return nil
	}); err != nil {
		return nil, err
	}
	obj := func(block int64, frame int) []byte { return frames[block][frame] }

	for id, p := range picks {
		o := obj(p.block, p.frame)
		if o == nil {
			s.Stats.SkippedRecords++
			continue
		}
		text, ok := readItemText(o, id, p.conv)
		if !ok {
			s.Stats.SkippedRecords++
			continue
		}
		m := Message{
			ID: hexID(id), ConversationID: hexID(p.conv), FolderID: hexID(p.folder), AccountID: hexID(folderAccount[p.folder]),
			Subject: text.Subject, Preview: text.Preview, MessageID: text.MessageID, ItemClass: text.Class,
			Read: p.status&0x02 != 0, HasAttachments: o[itemAttachAt]&0x40 != 0,
			Confirmed: p.confirmed, Ambiguous: p.ambiguous,
		}
		if v := binary.LittleEndian.Uint64(o[itemReplyToAt:]); ids.has(v) && v != id {
			m.ReplyToID = hexID(v)
		}
		if text.SenderAddr != "" || text.SenderName != "" {
			m.From = &Recipient{Name: text.SenderName, Address: text.SenderAddr}
		}
		m.Sent, m.Received = ticks(o, itemSentAt), ticks(o, itemReceivedAt)
		switch o[itemImportanceAt] {
		case 0:
			m.Importance = "low"
		case 1:
			m.Importance = "normal"
		case 2:
			m.Importance = "high"
		}
		for t, dst := range map[uint16]*[]Recipient{typeTo: &m.To, typeCc: &m.Cc, typeBcc: &m.Bcc} {
			if vs := lists[t][id]; len(vs) > 0 {
				v := pickVersion(vs)
				if ro := obj(v.block, v.frame); ro != nil {
					*dst = readRecipients(ro)
				}
			}
		}
		seenAtt := map[string]bool{}
		for _, v := range lists[typeAttachment][id] {
			if ao := obj(v.block, v.frame); ao != nil {
				if a, ok := readAttachment(ao); ok && !seenAtt[a.Name] {
					seenAtt[a.Name] = true
					m.Attachments = append(m.Attachments, a)
				}
			}
		}
		if !m.Confirmed {
			s.Stats.Unconfirmed++
		}
		if m.Ambiguous {
			s.Stats.Ambiguous++
		}
		s.Messages = append(s.Messages, m)
	}
	sort.Slice(s.Messages, func(i, j int) bool { return s.Messages[i].ID < s.Messages[j].ID })
	// Blocks keyed by an item vote for the item's account, like blocks of
	// its folders and of the root.
	owner := make(map[uint64]uint64, len(folderAccount)+len(picks))
	for f, a := range folderAccount {
		owner[f] = a
	}
	for id, p := range picks {
		if a, ok := folderAccount[p.folder]; ok {
			owner[id] = a
		}
	}
	s.buildAccounts(roots2roles(roots), dirVotes, owner)
	s.fillAddresses()
	return s, nil
}

type rootSeen struct {
	roles  map[string]uint64
	direct bool
}

func roots2roles(roots map[uint64]rootSeen) map[uint64]map[string]uint64 {
	out := make(map[uint64]map[string]uint64, len(roots))
	for id, r := range roots {
		out[id] = r.roles
	}
	return out
}
