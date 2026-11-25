# Outlook for Mac local storage (Phase 0 findings)

Discovered on one machine on 2026-11-24. The format is undocumented and can change with any Outlook update. Confidence labels: **verified** (checked on this machine, usually by a controlled experiment), **likely** (consistent with all observations, not proven), **unknown**.

The container layer follows the public specification by securized, [hxstore-reverse-engineering](https://github.com/securized/hxstore-reverse-engineering) (MIT, tested on 16.107.1). Everything below the container layer (object framing, version stamp, read state, flag, folders) is new work from this project.

## Client

| Item | Value | Confidence |
|---|---|---|
| Client | New Outlook for Mac (`defaults read com.microsoft.Outlook IsRunningNewOutlook` = `1`) | verified |
| Version | 16.115.1 (`CFBundleShortVersionString`) | verified |
| Legacy Outlook | Not usable. `Data/Outlook.sqlite` exists but has 0 rows in `Mail` and `AccountsExchange`; the WAL is empty; last change April 2025. Legacy Outlook stopped working with Exchange Online in October 2026. | verified |
| Legacy vs new consistency | Not applicable: only the new client holds data. | verified |
| macOS privacy prompt | Reading the Group Container from the terminal worked without a prompt on this machine. The tool must still detect `operation not permitted` and tell the user to grant Full Disk Access to the terminal app. | verified for this terminal only |

## Paths

Profile root: `~/Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles/<profile>/`. One profile here: `Main Profile`.

| Path | Content | Confidence |
|---|---|---|
| `HxStore.hxd` | The mail store (117 MB here). Mutated in place while Outlook runs. | verified |
| `HxStore.lock` | Held by Outlook. Never touch. | verified |
| `hxcore.hfl`, `hxcore_previous_session.hfl` | 50 MB binary, purpose unknown (log?). Not needed. | unknown |
| `Files/S0/<n>/` | One folder per account (`2` and `2948` here). Subfolders `EFMData`, `Attachments`, `Photos`, `MimeFiles`, `ContactPhotos`, `Data`, `NonPersisted`. | likely (account mapping not proven) |
| `Files/S0/<n>/EFMData/<number>.dat` | Large message bodies, HTML, gzip (86%) or plain. Every file decodes to >= 64 KB, so bodies of 64 KB and more live here instead of in the store. Names are sequential numbers. A 90 KB probe body produced 6 files (4 in `2`, 2 in `2948`). | verified (format), likely (64 KB rule) |
| `Files/S0/<n>/Attachments/` | Attachment content (out of scope). | likely |
| `Osa/OutlookServiceApiLogs_*/*.xmlgz` | About 24,000 gzipped XML protocol logs, about 10 days retention. Content is hashed (`pii:...`), field names are in clear. Used only as a schema oracle, never as a data source. | verified |
| `Data/Outlook.sqlite` | Legacy store, empty. | verified |

## Container: blocks

| Item | Finding | Confidence |
|---|---|---|
| Header | Magic `Nostromo`, version byte `i` at +8, page size 4096 at +0x38. | verified |
| Blocks | 40-byte header: CRC-32 of header bytes `[4,0x20)`, CRC-32 of `[8, end)`, magic `05 6a 70 3b 64 45 02 5d`, type u32, compressed length, decompressed length, constant 4, then **u64 key at +0x20**. Payload is one LZ4 block. | verified |
| Key at +0x20 | Equals the ID in the first object framing when the payload starts with one (5,345 of 5,345 such blocks; the other blocks start with a multi-object header). The spec lists this field as unresolved. | verified |
| Validity | First snapshot: 17,682 candidates, 17,639 valid, 43 invalid; identical across plain copies and APFS clones, so not caused by copying. Later snapshot: 138 invalid, of which 130 are **type 16 with a payload CRC mismatch** and 8 fail the header CRC (their type field cannot be trusted). Type 16 is a second block format that the reader does not decode yet. Until it is decoded, a reader that counts these as damage reports `partial = true` on every call. | verified (counts), unknown (type 16 format) |
| Versions | The store keeps old versions of each object. A key can appear in 10 or more valid blocks. CRC validity does not mean "current"; see the live-block index below. | verified |
| Header area pointers | u64 at header +0x58 = `0x25000` and +0x78 = `0x75000`: two areas of `0x50000` bytes each (A and B). Header +0x18 = `0x5000` and +0x20 = `0xc5000` probably bound the region `[0x5000, 0xc5000)`, which also holds data before area A. Blocks start after this region (first block at `0xc5000` in the last snapshot). | verified (values), likely (meaning) |
| Live-block index | Both areas contain `offset / 256` of live blocks as a u32 (every block starts on a 256-byte boundary). The entry layout is not decoded: the values sit at any byte alignment, density is uniform (probably a hash table), and the u16 after the value is not the block length. Test: 55% of valid blocks are referenced, against 0.32% of other 256-byte offsets (false-positive rate). Per message ID, **at most one copy is referenced** (1,872 IDs with exactly one, always the copy with the highest stamp; 570 IDs with none, probably deleted; 0 IDs with two or more). Areas A and B differed in bytes but gave the same result. Which area is current is unknown. **The index lags behind:** it is updated at checkpoints (the u64 at header +0x68 changes with each one; it stayed the same for several minutes while Outlook wrote new blocks). A message moved after the last checkpoint has a new, unindexed copy, and its old copy may also drop out of the index, so it has no indexed copy at all. Fallback under test: if the newest copy is not indexed and its stamp is >= the highest stamp among indexed copies, use it. This found the deleted probe, the new draft and the Junk move, but in one snapshot it also picked 17 Inbox messages whose status is unknown. | verified (membership test), unknown (entry layout, A/B choice) |

## Objects

| Item | Finding | Confidence |
|---|---|---|
| Framing | `06 00 70 05`, u64 object ID, u16 type, u32 length, then fields. Next comes `90 02`, the ID again, a u16 class (`0x4d` = folder) and the owner ID. 49,750 framings found, all lengths fit inside their payload. | verified |
| Payload header | 16 bytes before the first object, for example `00000000 00000000 01000000 <len-16>`. A second header style (`02000000 ...`) holds several objects. | likely |
| Object IDs | u64, values around `0x1875xxxx` to `0x1880xxxx`; new objects get higher IDs. References to other objects are raw u64 IDs. | verified |
| Strings | Length-prefixed: u32 byte length including the terminating NUL, OR `0x80000000`, then 2 bytes, then UTF-16LE text. This is why fields often have no visible separator. Seen on folder names. | verified (folder names), likely (all strings) |
| Message object | Type `0xc7`, in two forms. **Message form**: an ID-range reference at +32, the status byte at +1772, the stamp at +370. **Parent form**: 10 bytes shorter, shares the conversation-level fields, ID of the owner (probe 5: message `0x18809236`, parent `0x18809225`). The message-form ID stayed the same over all versions of probe 5 (read, flag, move). 2,433 message-form IDs in the last snapshot. | verified on probes |
| Folder objects | Several small objects per folder ID (types `0x3d`, `0x1e3`, `0x1cc`) plus a large `0xd6` object (probably the message list), all with class `0x4d` and the account root as owner. | likely |
| Folder names (older entry format) | Entries `4d` + 9 zero bytes, folder ID, `2`, owner, folder ID, owner, 4 zero bytes, length-prefixed name. Found only for a few folders. The earlier claim that standard folders have no stored name was wrong: see Folder records. | verified (few folders) |
| Account roots | `0x18757896` (account A) and `0x18757ba5` (account B) own the folders of each account. `0x18757694` (A) and `0x1875791b` (B) are a second per-account object, referenced at +380 of every message and next to folder entries. Mapping to `Files/S0/<n>` and to the account address is open. | verified (roots), likely (second object) |
| Standard folder slots | The block of an account root starts with an unframed record (about 2 KB before the first framing). It holds references `[type u16][8 zero bytes][ID][u32 2][owner][ID][owner]` at fixed offsets, so **the offset of the slot gives the folder role**. Both accounts use the same offsets. Verified roles: +861 = Inbox (probe mails received in A and B), +1549 = Sent (probe mails sent from A and B). +477 = Drafts (a new unsent `oc-probe-draft` in account A was stored in the +477 folder). +1205 = Outbox, from the move of one sent probe Drafts → Outbox → Sent (account A only). +429 = Deleted Items (a deleted probe moved there). +909 = Junk Email (a probe dragged to Junk moved there). +285 = Archive (a probe archived with the Archive button moved there). Not yet labelled: +621, +1397, +1445 (A only); the UI shows Conversation History in both accounts and Snoozed and RSS Subscriptions only in account A, so one of the two B slots is a folder the UI does not show. | verified (Inbox, Sent, Drafts, Deleted, Junk, Archive; account A, and Inbox and Sent in B), likely (Outbox), unknown (others) |
| Folder records and hierarchy | A parent's folder list (the account root's, or a parent folder's, `0x01e3` and `0x0002` objects) holds one record per child folder: `e3 01`, 8 zero bytes, **parent ID** (u64), a Windows collation sort key (pairs `0e xx`, separators `01`, ending with one `00` byte), the **display name** as UTF-16LE ending with `00 00`, then (form 1) `01`, 4 bytes, `05 00 d8 04`, 4 bytes, `00 00 4d`, 9 zero bytes, **folder ID**, u32 2, account root. Verified: the probe folder name; a user folder of account A holding its six subfolders, matched to the UI by name and by the unread count (the subfolder with the one unread message is `0x1875ab71`). Standard folders store English names (`Inbox`, `Drafts`, `Sent Items`, `Deleted Items`, `Archive`, `Junk Email`) that agree with the role slots. Form 2 records have the same head but no folder ID after the name (the next object header belongs to a different folder); four empty user folders and some standard ones (Outbox, Conversation History, Snoozed, RSS Subscriptions) were only found in this form or not at all. The older entry format (`4d` + 9 zero bytes + ID ... `0x80000000|len` name) gives ID and name for a few of them (one empty user folder of account B is `0x1875ac04`). Every folder that holds live messages in the last snapshot has a form 1 name and parent. | verified (form 1, on folders with messages), unknown (form 2 IDs) |
| Favorites list | Type `0x0163` of the second per-account object holds an ordered list of folder references (Inbox, Sent, then other folders). It differs per account, so it is a user list (probably Favorites), not a role table. | likely |
| Other types | `0x168`, `0x2f6`, `0xce`, `0xcd`, `0xcc`, `0x154` (about 3,700 each), `0x04` (one new `0x04` object appeared when a message was moved). Meaning unknown. | unknown |

## Message object (`0xc7`, message form) fields

Offsets are from the start of the object framing. The probes are 2 to 4 messages from one sender; the population checks cover all 2,433 message-form IDs.

| Field | Location and encoding | Confidence |
|---|---|---|
| Version stamp | 6 bytes at +370, little-endian. **It does not change when a message is marked read**: after the user marked about 20 probe copies read, 19 IDs had copies with the same highest stamp and different read bits. So "highest stamp" alone does not find the current copy. **Current copy = the copy whose block is in the live-block index** (see Container). 848 IDs have stamp 0 in every copy. The value is not unique per write; successive values differ by about N x 10^9. | verified (population and probes) |
| Other layout | 11 to 12 IDs pass the message-form test (ID at +32, length >= 1800) but have no folder ID at +1166, and no known folder ID within +1166 ±128. They are a different record layout, not a shifted field. The reader must skip them (counted in `skipped_records`) until the layout is known. They caused 7 of the earlier "8 unread". | verified |
| Status byte | +1772. Bit `0x02` = read: set by "mark read" and cleared by "mark unread" on probe 1, set by "mark read" on probe 5. Bit `0x80` is set on Inbox copies, absent on the sent copy, and was cleared when probe 5 was moved to a user folder, so it is **not** a delete marker; probably Focused Inbox (UNVERIFIED). Bit `0x10` unknown. **Population check passed:** with the live-block rule, the last snapshot has 2 unread messages, 1 in the Inbox of account A and 1 in one user folder of account A, and 0 in account B. The Outlook UI showed the same three numbers. The old highest-stamp rule gave 21. | verified (read bit, unread counts), unknown (`0x80`, `0x10`) |
| Current folder | u64 at +1166. Changed from `0x18757bbd` to the new folder `0x1880955b` when probe 5 was moved. A sent probe moved Drafts → Outbox → Sent (`0x187578c4` → `0x187578b9` → `0x18757dbd`). Live copies use 16 folder IDs, all of them real folders. | verified |
| Account root | u64 at +32 (`0x18757ba5` for probe 5). | likely |
| Flag status | Byte with values 0 = none, 2 = flagged (matches the Exchange FlagStatus enum; 1 = complete is probable). At +1742 on the probes. Across the store the byte at +1742 is often `0xb7` or `0x85`, so the flag position depends on the layout. | verified on probe (value), unknown (generic location) |
| Strings | Sender address, sender display name, `IPM.Note` item class, Message-ID, preview or body, subject twice (`NormalizedSubject`, `Topic`). | verified (order on probes) |
| HTML body | Small bodies are stored inline as UTF-8 HTML inside the object; large bodies (>= 64 KB) are in `EFMData`. | verified (both cases seen) |
| Dates | .NET ticks (100 ns since 0001-01-01 UTC), u64 little-endian, several per object (sent, received, modified). Which offset is which is not mapped yet. | verified (encoding), unknown (offsets) |
| Item class strings | 15,877 `IPM.Note` strings, about 8,700 meeting-item strings, 93 out-of-office templates, 1 NDR. These count string occurrences over all versions and forms, not messages. | verified (as string counts) |
| To/Cc split, importance, thread ID, internet headers | Not mapped. The Osa logs show that the protocol carries `To`, `Cc`, `Importance`, `HasAttachment`, `ThreadId`, `ConversationId`. Internet headers are not stored (spec). | unknown |
| Attachments | The attachment file name (UTF-16LE) is stored in its own object near the message. Size, content type and the link to the message are not mapped. | likely (name), unknown (rest) |
| Link to `EFMData` file | Not found as u32 or text in payloads that contain the message subject. A search of all payloads found the file numbers in type `0x2cb` at +104 and +112 and in type `0x2e0`; not followed up. | unknown |

## Snapshot method

| Method | Result | Confidence |
|---|---|---|
| Streaming copy (`os.Open` read-only, copy to `os.MkdirTemp` 0700, retry when size or mtime changes) | Consistent: same block and damage counts as the clone. About 50 ms for 117 MB when the file is in the page cache. | verified |
| APFS clone (`cp -c`) | Same result, 30 ms. Needs `clonefile(2)`; not in the Go standard library. | verified |
| Decision | Streaming copy. No extra dependency. Size and mtime are checked on the open file before and after the copy (3 attempts). The per-block CRCs catch a torn block, but not a copy that mixes valid blocks written at different moments; with 138 standing CRC failures, a new failure caused by copying cannot be told apart either. | decision |

The live file changed between snapshots taken seconds apart, even with no user action (a stamp field changed in every copy).

## Body coverage

| Tier | Finding | Confidence |
|---|---|---|
| Preview only | About 255 characters (spec: 89% of messages on the reference store). | not measured here |
| Inline HTML | Present for small bodies the client downloaded. | verified on probes |
| `EFMData` | 2,128 files before the experiment; all bodies >= 64 KB. | verified |

## Privacy notes for development

- `hxdiscover` (dev tool, not shipped) prints counts, offsets, hex of binary bytes and redacted shapes of text. Hex output masks every span that decodes as UTF-16LE or UTF-8 text in scripts below U+2000 (Latin, Cyrillic, Greek, Arabic, Hebrew and others). CJK and Hangul text is **not** detected, because object IDs often decode to CJK code points.
- Text is printed only from the `oc-probe` marker onward, and only while it stays in the synthetic character set (lower-case letters, digits, space and `-.:,_`); markers must start with `oc-probe`. Exact times of recent ticks are printed only with `-ticks`.
- `namefind` and `tree -marker` accept a real folder name typed by the developer, to match it against the store. They print only IDs, types, offsets and "equals" markers, never the stored text.
- Three incidents during discovery, all fixed in the tool: item-class strings glued to a following name field printed about 150 display names; a hex probe printed part of the user's own name as UTF-16 hex; a misparsed folder entry printed the user's own display name next to probe text.

## Open questions

1. Generic field location inside `0xc7` (flag, dates, recipients): walk the property layout instead of fixed offsets.
2. Type-16 block format.
3. Roles of slots +621, +1397, +1445 (Conversation History, Snoozed and one folder the UI does not show); folder IDs of form 2 records (only empty folders so far); the reader rule for changes made after the last index checkpoint, folder hierarchy, unread and total counts per folder. Layout of the live-block index entries, and which of areas A and B is current.
4. Account mapping: account roots to `Files/S0/<n>` and to the account email.
5. To/Cc lists, importance, attachment metadata, thread/conversation ID, date offsets.
6. Link from message to `EFMData` file.
7. Deleted items: what a delete changes (folder reference to Deleted Items, or more).
8. Meaning of status bits `0x80` and `0x10`, and of the new `0x04` object after a move.
