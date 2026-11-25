package store

import (
	"encoding/binary"
	"fmt"
	"sort"
	"time"
)

const ticksUnixEpoch = 621355968000000000

func hexID(v uint64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%x", v)
}

// ticks reads a .NET ticks u64 (100 ns since 0001-01-01 UTC); nil when the
// value is not a plausible mail time (1995 to 2100).
func ticks(obj []byte, at int) *time.Time {
	if at < 0 || at+8 > len(obj) {
		return nil
	}
	v := binary.LittleEndian.Uint64(obj[at:])
	if v < ticksUnixEpoch {
		return nil
	}
	d := v - ticksUnixEpoch
	t := time.Unix(int64(d/10_000_000), int64(d%10_000_000)*100).UTC()
	if t.Year() < 1995 || t.Year() > 2100 {
		return nil
	}
	return &t
}

// byNumber orders hex IDs (no leading zeros) by their numeric value.
func byNumber(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// buildFolders picks one version of every folder record (a version in a
// direct index slot wins), keeps folders of mail accounts and returns the
// folder -> account map.
func (s *Store) buildFolders(recs []folderRec, ids idSpace, roots map[uint64]map[string]uint64) map[uint64]uint64 {
	best := map[uint64]folderRec{}
	for _, r := range recs {
		if !ids.has(r.id) || !ids.has(r.parent) {
			continue
		}
		if old, ok := best[r.id]; !ok || r.direct || !old.direct {
			best[r.id] = r
		}
	}
	roleOf := map[uint64]string{}
	for _, roles := range roots {
		for role, id := range roles {
			roleOf[id] = role
		}
	}
	folders := map[uint64]Folder{}
	account := map[uint64]uint64{}
	for id, r := range best {
		if _, mail := roots[r.owner]; !mail {
			continue
		}
		f := Folder{ID: hexID(id), AccountID: hexID(r.owner), Name: r.name, Role: roleOf[id]}
		if r.parent != r.owner {
			f.ParentID = hexID(r.parent)
		}
		folders[id] = f
		account[id] = r.owner
	}
	// Role folders without a stored record still belong to their account.
	for root, roles := range roots {
		for role, id := range roles {
			if _, ok := folders[id]; !ok {
				folders[id] = Folder{ID: hexID(id), AccountID: hexID(root), Role: role}
				account[id] = root
			}
		}
	}
	for _, f := range folders {
		s.Folders = append(s.Folders, f)
	}
	sort.Slice(s.Folders, func(i, j int) bool { return byNumber(s.Folders[i].ID, s.Folders[j].ID) })
	return account
}

// buildAccounts creates one account per root with role slots. Its Files/S0
// folder number is the most frequent number in paths stored in blocks of
// the root, of its folders or of its items.
func (s *Store) buildAccounts(roots map[uint64]map[string]uint64, votes map[uint64]map[string]int, owner map[uint64]uint64) {
	dirs := map[uint64]map[string]int{}
	for key, vs := range votes {
		acc, ok := owner[key]
		if _, root := roots[key]; root {
			acc, ok = key, true
		}
		if !ok {
			continue
		}
		if dirs[acc] == nil {
			dirs[acc] = map[string]int{}
		}
		for d, n := range vs {
			dirs[acc][d] += n
		}
	}
	for root, roles := range roots {
		a := Account{ID: hexID(root), Roles: map[string]string{}}
		for role, id := range roles {
			a.Roles[role] = hexID(id)
		}
		best := 0
		for d, n := range dirs[root] {
			if n > best || n == best && d < a.Dir {
				a.Dir, best = d, n
			}
		}
		s.Accounts = append(s.Accounts, a)
	}
	sort.Slice(s.Accounts, func(i, j int) bool { return byNumber(s.Accounts[i].ID, s.Accounts[j].ID) })
}

// fillAddresses sets each account's address to the most frequent sender
// address of the messages in its Sent folder.
func (s *Store) fillAddresses() {
	for i := range s.Accounts {
		sent := s.Accounts[i].Roles["sent"]
		count := map[string]int{}
		for _, m := range s.Messages {
			if m.FolderID == sent && m.From != nil && m.From.Address != "" {
				count[m.From.Address]++
			}
		}
		best := 0
		for addr, n := range count {
			if n > best || n == best && addr < s.Accounts[i].Address {
				s.Accounts[i].Address, best = addr, n
			}
		}
	}
}
