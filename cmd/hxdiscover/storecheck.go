package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/store"
)

// storeCheck runs the real loader (internal/store) and prints counts only:
// accounts with their Files/S0 numbers and role counts, folders and how
// many message fields are filled. Probe subjects are printed for probes.
func storeCheck(data []byte, profile string, w io.Writer) error {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return err
	}
	for _, a := range s.Accounts {
		_, _ = fmt.Fprintf(w, "account %s dir=%s roles=%d address_found=%t\n", a.ID, a.Dir, len(a.Roles), a.Address != "")
	}
	named := 0
	for _, f := range s.Folders {
		if f.Name != "" {
			named++
		}
	}
	_, _ = fmt.Fprintf(w, "folders=%d named=%d\n", len(s.Folders), named)
	c := map[string]int{}
	for _, m := range s.Messages {
		c["messages"]++
		for name, ok := range map[string]bool{
			"msg subject": m.Subject != "", "from": m.From != nil, "to": len(m.To) > 0, "sent": m.Sent != nil, "received": m.Received != nil,
			"msg preview": m.Preview != "", "message_id": m.MessageID != "", "account": m.AccountID != "", "conversation": m.ConversationID != "",
			"read": m.Read, "reply_to": m.ReplyToID != "", "has_attachments": m.HasAttachments, "attachments": len(m.Attachments) > 0, "confirmed": m.Confirmed,
		} {
			if ok {
				c["field set: "+name]++
			}
		}
		if strings.HasPrefix(strings.ToLower(m.Subject), markerPrefix) {
			_, _ = fmt.Fprintf(w, "probe %s folder=%s read=%t to=%d cc=%d attachments=%d confirmed=%t subject=%s\n",
				m.ID, m.FolderID, m.Read, len(m.To), len(m.Cc), len(m.Attachments), m.Confirmed, probeText(m.Subject))
		}
	}
	for _, m := range s.Messages {
		c["body kind: "+m.BodyKind]++
		ref, err := s.Body(m.ID)
		switch {
		case err != nil:
			c["body: error"]++
		case ref.Kind != m.BodyKind:
			c["body: kind differs on demand"]++
		case ref.Kind == store.BodyExternal:
			c[fmt.Sprintf("body: efmdata files %d", min(len(ref.File), 3))]++
			b, _, err := body.ReadEFM(profile, ref.Dir, ref.File, 0)
			switch {
			case errors.Is(err, body.ErrNoFile):
				c["body: efmdata no readable file"]++
			case err != nil:
				c["body: efmdata error"]++
			default:
				c[fmt.Sprintf("body: efmdata read, html document=%t", isHTMLDoc(b))]++
			}
		}
	}
	printCounts(w, "messages (counts only)", c)
	_, _ = fmt.Fprintf(w, "stats: valid=%d undecoded=%d damage=%d unindexed_damaged=%d skipped=%d unconfirmed=%d ambiguous=%d index_areas_differ=%d\n",
		s.Stats.ValidBlocks, s.Stats.UndecodedBlocks, len(s.Stats.Damage), s.Stats.UnindexedDamaged, s.Stats.SkippedRecords,
		s.Stats.Unconfirmed, s.Stats.Ambiguous, s.Stats.IndexAreasDiffer)
	return nil
}
