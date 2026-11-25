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

// ErrUnknownMessage is returned for a message ID that the store does not
// list.
var ErrUnknownMessage = errors.New("unknown message")

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
	// BodyKind says where the body is stored: inline, efmdata, none or
	// unparsed (see Store.Body).
	BodyKind string `json:"body_kind"`
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
	Damage          []hxstore.Damage `json:"damage,omitempty"` // failing blocks in a direct index slot
	// UnindexedDamaged counts failing blocks without a direct index slot.
	// Most are old copies, but a live block whose index entry sits in an
	// overflow bucket is counted here too.
	UnindexedDamaged int `json:"unindexed_damaged"`
	// SkippedRecords counts items that have no copy with a known folder or
	// whose chosen copy has no readable string run.
	SkippedRecords int `json:"skipped_records"`
	Unconfirmed    int `json:"unconfirmed"`
	Ambiguous      int `json:"ambiguous"`
	// IndexUnreadable is true when the block index has an unexpected
	// layout; every message is then unconfirmed.
	IndexUnreadable bool `json:"index_unreadable,omitempty"`
	// IndexAreasDiffer counts index slots where areas A and B differ; the
	// reader uses area A (which area is current is UNVERIFIED).
	IndexAreasDiffer int `json:"index_areas_differ"`
}

// Store is the decoded content of one HxStore.hxd snapshot. It keeps the
// snapshot bytes to read bodies on demand.
type Store struct {
	Accounts []Account
	Folders  []Folder
	Messages []Message
	Stats    Stats

	data      []byte
	itemBlock map[uint64]int64 // message ID -> block of the chosen copy
}

type objVersion struct {
	block  int64
	frame  int
	direct bool
}

// versionBlock chooses the block whose versions of an item's list or
// attachment objects are used: the block of the chosen item copy when it
// holds a version, else the first block in a direct index slot, else the
// last block in file order.
func versionBlock(vs []objVersion, itemBlock int64) int64 {
	for _, v := range vs {
		if v.block == itemBlock {
			return itemBlock
		}
	}
	for _, v := range vs {
		if v.direct {
			return v.block
		}
	}
	return vs[len(vs)-1].block
}

// inBlock returns the versions stored in block.
func inBlock(vs []objVersion, block int64) []objVersion {
	var out []objVersion
	for _, v := range vs {
		if v.block == block {
			out = append(out, v)
		}
	}
	return out
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
	s := &Store{data: data, itemBlock: map[uint64]int64{}}
	ix, err := hxstore.ReadIndex(data)
	switch {
	case errors.Is(err, hxstore.ErrIndexLayout):
		s.Stats.IndexUnreadable = true
	case err != nil:
		return nil, err
	}
	s.Stats.IndexAreasDiffer = ix.AreasDiffer

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
			r.direct = direct
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
	s.Stats.ValidBlocks = st.Valid
	for _, d := range damage {
		switch {
		case d.BlockType == 16:
			s.Stats.UndecodedBlocks++
		case ix.Direct(d.Offset):
			s.Stats.Damage = append(s.Stats.Damage, d)
		default:
			s.Stats.UnindexedDamaged++
		}
	}

	roles := roots2roles(roots)
	folderAccount := s.buildFolders(recs, ids, roles)

	// Choose the current copy of every item.
	copies := map[uint64][]itemCopy{}
	noFolder := map[uint64]bool{}
	for _, r := range raws {
		switch {
		case ids.has(r.account):
			continue // conversation form
		case !ids.has(r.folder):
			noFolder[r.id] = true
			continue
		}
		copies[r.id] = append(copies[r.id], r.itemCopy)
	}
	for id := range noFolder {
		if _, ok := copies[id]; !ok {
			s.Stats.SkippedRecords++
		}
	}
	type chosen struct {
		itemCopy
		confirmed, ambiguous bool
	}
	picks := make(map[uint64]chosen, len(copies))
	// chosenVers holds, per item, the list and attachment versions to read.
	chosenVers := make(map[uint64]map[uint16][]objVersion, len(copies))
	need := map[int64]map[int]bool{} // block -> framing offsets to keep
	want := func(block int64, frame int) {
		if need[block] == nil {
			need[block] = map[int]bool{}
		}
		need[block][frame] = true
	}
	for id, cs := range copies {
		c, conf, amb := pickCurrent(cs)
		picks[id] = chosen{c, conf, amb}
		want(c.block, c.frame)
		for _, t := range []uint16{typeTo, typeCc, typeBcc, typeAttachment} {
			vs := lists[t][id]
			if len(vs) == 0 {
				continue
			}
			sel := inBlock(vs, versionBlock(vs, c.block))
			if chosenVers[id] == nil {
				chosenVers[id] = map[uint16][]objVersion{}
			}
			chosenVers[id][t] = sel
			for _, v := range sel {
				want(v.block, v.frame)
			}
		}
	}

	// Pass 2: decode only the blocks that hold chosen objects and keep only
	// those objects.
	itemsIn := map[int64][]uint64{} // block -> items whose chosen copy it holds
	for id, p := range picks {
		itemsIn[p.block] = append(itemsIn[p.block], id)
	}
	bodyKind := make(map[uint64]string, len(picks))
	frames := map[int64]map[int][]byte{}
	for block, offs := range need {
		b, err := hxstore.DecodeAt(data, block)
		if err != nil {
			return nil, err
		}
		for _, id := range itemsIn[block] {
			kind := BodyUnparsed
			if b.Key == id { // the body record is in the item's own block
				kind = ParseBody(b.Payload).Kind
			}
			bodyKind[id] = kind
		}
		m := map[int][]byte{}
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) {
			if offs[f.Offset] {
				m[f.Offset] = f.Data
			}
		})
		frames[block] = m
	}
	obj := func(block int64, frame int) []byte { return frames[block][frame] }

	for id, p := range picks {
		o := obj(p.block, p.frame)
		if o == nil {
			s.Stats.SkippedRecords++
			continue
		}
		text, ok := ParseItemText(o, id, p.conv) // the anchor holds the raw value
		if !ok {
			s.Stats.SkippedRecords++
			continue
		}
		conv := p.conv
		if !ids.has(conv) {
			conv = 0
		}
		m := Message{
			ID: hexID(id), ConversationID: hexID(conv), FolderID: hexID(p.folder), AccountID: hexID(folderAccount[p.folder]),
			Subject: text.Subject, Preview: text.Preview, MessageID: text.MessageID, ItemClass: text.Class,
			Read: p.status&0x02 != 0, HasAttachments: o[itemAttachAt]&0x40 != 0,
			Confirmed: p.confirmed, Ambiguous: p.ambiguous, BodyKind: bodyKind[id],
		}
		s.itemBlock[id] = p.block
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
		vers := chosenVers[id]
		for t, dst := range map[uint16]*[]Recipient{typeTo: &m.To, typeCc: &m.Cc, typeBcc: &m.Bcc} {
			for _, v := range vers[t] {
				if ro := obj(v.block, v.frame); ro != nil {
					*dst = ParseRecipients(ro)
					break
				}
			}
		}
		seenAtt := map[Attachment]bool{}
		for _, v := range vers[typeAttachment] {
			if ao := obj(v.block, v.frame); ao != nil {
				if a, ok := ParseAttachment(ao); ok && !seenAtt[a] {
					seenAtt[a] = true
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
	sort.Slice(s.Messages, func(i, j int) bool { return byNumber(s.Messages[i].ID, s.Messages[j].ID) })
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
	s.buildAccounts(roles, dirVotes, owner)
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
