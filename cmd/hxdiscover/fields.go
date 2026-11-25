package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
)

// Item field offsets (item form; see docs/STORAGE.md).
const (
	itemReceivedAt   = 536
	itemSentAt       = 976
	itemImportanceAt = 1456
	itemAttachAt     = 1758
)

// itemStrings parses the string area of an item: after the anchor
// [item ID][00 00 c7 00 + 8 zero bytes][conversation ID] come NUL-terminated
// UTF-16LE strings: sender address, sender name, item class, Message-ID,
// subject, topic. ok is false when the anchor is missing.
func itemStrings(obj []byte, id, conv uint64) (s []string, ok bool) {
	var anchor []byte
	anchor = binary.LittleEndian.AppendUint64(anchor, id)
	anchor = append(anchor, 0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	anchor = binary.LittleEndian.AppendUint64(anchor, conv)
	i := bytes.Index(obj, anchor)
	if i < 0 {
		return nil, false
	}
	p := i + len(anchor)
	for len(s) < maxItemStrings && p+2 <= len(obj) {
		e := p
		for e+2 <= len(obj) && (obj[e] != 0 || obj[e+1] != 0) {
			e += 2
		}
		s = append(s, decodeU16(obj[p:e]))
		p = e + 2
	}
	return s, len(s) >= 6
}

const maxItemStrings = 32

// fieldCheck tests every fixed-offset field and the string area on the
// current copy of every item and prints only counts.
func fieldCheck(data []byte, now time.Time, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	convs, err := currentC7(data, li, true)
	if err != nil {
		return err
	}
	recs, _, err := folderRecords(data)
	if err != nil {
		return err
	}
	folders := map[uint64]bool{}
	for id := range recs {
		folders[id] = true
	}
	lo := time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC)
	hi := now.Add(24 * time.Hour)
	tick := func(obj []byte, at int) (time.Time, bool) {
		v := binary.LittleEndian.Uint64(obj[at:])
		if v < ticksUnixEpoch {
			return time.Time{}, false
		}
		t := ticksToTime(v)
		return t, t.After(lo) && t.Before(hi)
	}
	c := map[string]int{}
	for _, it := range cur {
		o := it.obj
		c["items"]++
		if folders[it.folder] {
			c["folder: has a folder record"]++
		}
		if _, ok := convs[it.conv]; ok {
			c["conversation: is a current conversation object"]++
		}
		switch imp := o[itemImportanceAt]; imp {
		case 0, 1, 2:
			c[fmt.Sprintf("importance=%d", imp)]++
		default:
			c["importance: other value"]++
		}
		if o[itemAttachAt]&0x40 != 0 {
			c["has-attachments bit set"]++
		}
		sent, okS := tick(o, itemSentAt)
		recv, okR := tick(o, itemReceivedAt)
		switch {
		case okS && okR && !recv.Before(sent.Add(-time.Hour)):
			c["dates: sent and received plausible, received >= sent - 1h"]++
		case okS && okR:
			c["dates: both plausible, received before sent - 1h"]++
		case okS:
			c["dates: only sent plausible"]++
		case okR:
			c["dates: only received plausible"]++
		default:
			c["dates: neither plausible"]++
		}
		s, ok := itemStrings(o, it.id, it.conv)
		if !ok {
			c["strings: anchor missing or fewer than 6"]++
			continue
		}
		c["strings: parsed"]++
		classAt, idAt := -1, -1
		for k, v := range s {
			if classAt < 0 && (strings.HasPrefix(v, "IPM.") || strings.HasPrefix(v, "REPORT.")) {
				classAt = k
			}
			if idAt < 0 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") && strings.Contains(v, "@") {
				idAt = k
			}
		}
		c[fmt.Sprintf("layout: class at string %d, Message-ID at string %d", classAt, idAt)]++
		if idAt >= 0 && idAt+2 < len(s) {
			subj, topic := s[idAt+1], s[idAt+2]
			switch {
			case subj == topic:
				c["after Message-ID: subject == topic"]++
			case strings.HasSuffix(topic, subj) || strings.HasSuffix(subj, topic):
				c["after Message-ID: one ends with the other (prefix like RE:)"]++
			default:
				c["after Message-ID: unrelated strings"]++
			}
		}
		if classAt == idAt-1 {
			c["Message-ID right after class"]++
		}
		if classAt >= 0 {
			shape := ""
			for k := 0; k < classAt; k++ {
				switch {
				case s[k] == "":
					shape += "E"
				case strings.Contains(s[k], "@"):
					shape += "@"
				default:
					shape += "T"
				}
			}
			c["layout: strings before class (E empty, @ address, T text) = "+shape]++
			cls := "other class"
			if safeItemClass(s[classAt]) {
				cls = s[classAt]
			}
			c[fmt.Sprintf("class %s with layout %q", cls, shape)]++
			if shape == "T@TT" || shape == "T@TTT" {
				for k := 0; k < classAt; k++ {
					if k == 1 {
						continue
					}
					n := len([]rune(s[k]))
					bucket := fmt.Sprintf("len %d", n)
					if n > 12 {
						bucket = "len > 12"
					}
					sp := " no space"
					if strings.Contains(s[k], " ") {
						sp = " has space"
					}
					up := " lower"
					if strings.ToUpper(s[k]) == s[k] {
						up = " all upper"
					}
					c[fmt.Sprintf("shape %s string %d: %s%s%s", shape, k, bucket, sp, up)]++
				}
			}
		}
		if strings.Contains(s[0], "@") || s[0] == "" {
			c["strings: sender address has @ or is empty"]++
		}
		if strings.HasPrefix(s[2], "IPM.") || strings.HasPrefix(s[2], "REPORT.") {
			c["strings: item class starts with IPM. or REPORT."]++
		}
		if s[3] == "" || strings.HasPrefix(s[3], "<") && strings.HasSuffix(s[3], ">") {
			c["strings: Message-ID is <...> or empty"]++
		}
		if s[4] == s[5] || strings.HasSuffix(s[5], s[4]) {
			c["strings: topic ends with subject"]++
		}
	}
	printCounts(w, "item field checks (counts only)", c)
	return nil
}

// Length table offsets (u16, UTF-16 bytes + 2) in items.
const (
	lenSenderAddr = 1648
	lenSenderName = 1640
	lenPreview    = 1556
	lenSubject    = 1512
	lenTopic      = 1684
	lenMessageID  = 1480
)

type itemText struct {
	senderAddr, senderName, class, messageID, preview, subject, topic string
	ok                                                                map[string]bool // field resolved by the rules
}

// extractText reads the string run of an item with the length table: the
// item class is the first string that starts with "IPM." or "REPORT.";
// every other field is the first string (before the class for the sender,
// after it for the rest) whose UTF-16 size + 2 equals that field's length
// entry. The subject falls back to the topic length when no string matches
// the subject length.
func extractText(obj []byte, id, conv uint64) (itemText, bool) {
	t := itemText{ok: map[string]bool{}}
	s, ok := itemStrings(obj, id, conv)
	if !ok {
		return t, false
	}
	u16 := func(off int) int { return int(binary.LittleEndian.Uint16(obj[off:])) }
	size := func(x string) int { return len(encodeU16(x)) + 2 }
	classAt := -1
	for k, x := range s {
		if strings.HasPrefix(x, "IPM.") || strings.HasPrefix(x, "REPORT.") {
			classAt = k
			break
		}
	}
	if classAt < 0 {
		return t, false
	}
	t.class, t.ok["class"] = s[classAt], true
	used := map[int]bool{classAt: true}
	find := func(from, to, length int, accept func(string) bool) (string, bool) {
		if length <= 2 {
			return "", false
		}
		step := 1
		if from > to {
			step = -1
		}
		for k := from; k != to; k += step {
			if k >= 0 && k < len(s) && !used[k] && size(s[k]) == length && (accept == nil || accept(s[k])) {
				used[k] = true
				return s[k], true
			}
		}
		return "", false
	}
	isID := func(x string) bool { return strings.HasPrefix(x, "<") && strings.HasSuffix(x, ">") }
	t.senderAddr, t.ok["senderAddr"] = find(classAt-1, -1, u16(lenSenderAddr), func(x string) bool { return strings.Contains(x, "@") })
	t.senderName, t.ok["senderName"] = find(classAt-1, -1, u16(lenSenderName), nil)
	t.messageID, t.ok["messageID"] = find(classAt+1, len(s), u16(lenMessageID), isID)
	t.preview, t.ok["preview"] = find(classAt+1, len(s), u16(lenPreview), nil)
	either := func(length int) (string, bool) { // after the class first, then before it
		if v, ok := find(classAt+1, len(s), length, nil); ok {
			return v, true
		}
		return find(classAt-1, -1, length, nil)
	}
	t.subject, t.ok["subject"] = either(u16(lenSubject))
	t.topic, t.ok["topic"] = either(u16(lenTopic))
	if !t.ok["subject"] && t.ok["topic"] {
		t.subject, t.ok["subject (from topic)"] = t.topic, true
	}
	return t, true
}

// textCheck runs extractText on all current items and prints how often
// each field resolves. For probe items it also prints whether the subject
// equals the expected synthetic text start (the subject itself is printed
// only for probes).
func textCheck(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	c := map[string]int{}
	for _, it := range cur {
		t, ok := extractText(it.obj, it.id, it.conv)
		c["items"]++
		if !ok {
			c["no class or run"]++
			continue
		}
		for _, f := range []string{"class", "messageID", "senderAddr", "senderName", "preview", "subject", "subject (from topic)", "topic"} {
			if t.ok[f] {
				c["resolved: "+f]++
			}
		}
		if probeSubject(it.obj) {
			subj := t.subject
			if !probeFolderName.MatchString(strings.ReplaceAll(strings.ToLower(subj), " ", "-")) {
				subj = shape(subj)
			}
			_, _ = fmt.Fprintf(w, "probe item %x subject=%s subject_from_topic=%t senderAddr=%t senderName=%t messageID=%t preview=%t\n",
				it.id, subj, t.ok["subject (from topic)"], t.ok["senderAddr"], t.ok["senderName"], t.ok["messageID"], t.ok["preview"])
		}
	}
	printCounts(w, "text extraction (counts only)", c)
	return nil
}
