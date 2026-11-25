package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/stn1slv/outlookcache/internal/store"
)

// storeCheck runs the real loader (internal/store) and prints counts only:
// accounts with their Files/S0 numbers and role counts, folders and how
// many message fields are filled. Probe subjects are printed for probes.
func storeCheck(data []byte, w io.Writer) error {
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
			"read": m.Read, "has_attachments": m.HasAttachments, "attachments": len(m.Attachments) > 0, "confirmed": m.Confirmed,
		} {
			if ok {
				c["field set: "+name]++
			}
		}
		if strings.HasPrefix(strings.ToLower(m.Subject), markerPrefix) {
			_, _ = fmt.Fprintf(w, "probe %s folder=%s read=%t to=%d cc=%d attachments=%d confirmed=%t subject=%q\n",
				m.ID, m.FolderID, m.Read, len(m.To), len(m.Cc), len(m.Attachments), m.Confirmed, m.Subject)
		}
	}
	printCounts(w, "messages (counts only)", c)
	_, _ = fmt.Fprintf(w, "stats: valid=%d undecoded=%d damage=%d stale_damaged=%d skipped=%d unconfirmed=%d ambiguous=%d\n",
		s.Stats.ValidBlocks, s.Stats.UndecodedBlocks, len(s.Stats.Damage), s.Stats.StaleDamaged, s.Stats.SkippedRecords, s.Stats.Unconfirmed, s.Stats.Ambiguous)
	return nil
}
