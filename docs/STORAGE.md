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
| Validity | First snapshot: 17,682 candidates, 17,639 valid, 43 invalid; identical across plain copies and APFS clones, so not caused by copying. Later snapshot: 138 invalid, of which 130 are **type 16 with a payload CRC mismatch** and 8 fail the header CRC (their type field cannot be trusted). **Type 16 is most likely freed space:** the count grew from 130 to 1,122 while Outlook ran, and only 3 of 1,122 are in the live-block index (the false-positive rate). So a reader should count them separately and not report them as damaged sections. | verified (counts), likely (type 16 = freed) |
| Versions | The store keeps old versions of each object. A key can appear in 10 or more valid blocks. CRC validity does not mean "current"; see the live-block index below. | verified |
| Header area pointers | u64 at header +0x58 = `0x25000` and +0x78 = `0x75000`: two areas of `0x50000` bytes each (A and B). Header +0x18 = `0x5000` and +0x20 = `0xc5000` probably bound the region `[0x5000, 0xc5000)`, which also holds data before area A. Blocks start after this region (first block at `0xc5000` in the last snapshot). | verified (values), likely (meaning) |
| Live-block index | Both areas contain `offset / 256` of live blocks as a u32 (every block starts on a 256-byte boundary). The entry layout is not decoded: the values sit at any byte alignment, density is uniform (probably a hash table), and the u16 after the value is not the block length. So this is a **candidate membership test**, not a parsed index: 55% of valid blocks match, against 0.32% of other 256-byte offsets (false positives), and a stale copy can match by chance. Per message ID, at most one copy matched (1,872 IDs with exactly one, always the copy with the highest stamp; 570 with none, probably deleted; 0 with two or more). Areas A and B differed in bytes but gave the same result; which one is current is unknown, and the tools use area A. **The index lags behind:** it is updated at checkpoints (the u64 at header +0x68 changes with each one; it stayed the same for several minutes while Outlook wrote new blocks). A message changed after the last checkpoint has a new, unindexed copy, and its old copy may also drop out of the index. A mark-read after the checkpoint keeps the stamp, so it shows only as an unindexed copy with the same stamp and another status; the tools report such IDs as ambiguous. **Reader rule (current copy), tested on items:** the indexed copy; if no copy is indexed, the newest copy only when its stamp is >= the checkpoint (the highest stamp among indexed copies), else the item is treated as deleted. With this rule the Inbox unread count matched the UI (2); without the deletion step it was 6 to 7, because deleted items keep unread copies. Limit: the membership test also misses some live blocks: a probe moved to Junk hours earlier has its newest copy in a normal, aligned block that is not indexed, so the rule drops it. Until the index entry layout is decoded, the reader must report such items in `skipped_records`. | verified (membership test), unknown (entry layout, A/B choice) |

## Objects

| Item | Finding | Confidence |
|---|---|---|
| Framing | `06 00 70 05`, u64 object ID, u16 type, u32 length, then fields. Next comes `90 02`, the ID again, a u16 class (`0x4d` = folder) and the owner ID. 49,750 framings found, all lengths fit inside their payload. | verified |
| Payload header | 16 bytes before the first object, for example `00000000 00000000 01000000 <len-16>`. A second header style (`02000000 ...`) holds several objects. | likely |
| Object IDs | u64, values around `0x1875xxxx` to `0x1880xxxx`; new objects get higher IDs. References to other objects are raw u64 IDs. The tools treat `[0x18000000, 0x19000000)` as the ID range; this range comes from one store and will not fit other profiles or a store that grows past it (UNVERIFIED). The reader needs a range taken from the store itself. | verified (values), unknown (general range) |
| Strings | Length-prefixed: u32 byte length including the terminating NUL, OR `0x80000000`, then 2 bytes, then UTF-16LE text. This is why fields often have no visible separator. Seen on folder names. | verified (folder names), likely (all strings) |
| Message and conversation objects | Type `0xc7`, in two forms. **Correction (reply probe):** the form with an account ID at +32, called "message form" in earlier notes, is the **conversation** object; the form without it ("parent form") is the **message item**. Proof: the reply item and the original item both point at +436 to the same conversation-form ID, in both accounts. A conversation has the same fields 10 bytes higher (folder +1166, status +1772, stamp +370) and follows its latest item, which is why single-message tests worked on it. The store has 4,041 items in 2,803 conversations (2,311 with one item). **The reader must list items, not conversations.** Offsets in the field table below are conversation-form offsets; subtract 10 for items (folder +1156, status +1762, stamp +360). | verified (reply probe) |
| Folder objects | Several small objects per folder ID (types `0x3d`, `0x1e3`, `0x1cc`) plus a large `0xd6` object (probably the message list), all with class `0x4d` and the account root as owner. | likely |
| Folder names (older entry format) | Entries `4d` + 9 zero bytes, folder ID, `2`, owner, folder ID, owner, 4 zero bytes, length-prefixed name. Found only for a few folders. The earlier claim that standard folders have no stored name was wrong: see Folder records. | verified (few folders) |
| Account roots | `0x18757896` (account A) and `0x18757ba5` (account B) own the folders of each account. `0x18757694` (A) and `0x1875791b` (B) are a second per-account object, referenced at +380 of every message and next to folder entries. | verified (roots), likely (second object) |
| Account to `Files/S0/<n>` | Objects of an account store absolute file paths as UTF-16 text: the account B root (`0x02e0` object) holds `.../Files/S0/2948/EFMData/<n>.dat` paths, and blocks of account A objects hold `.../Files/S0/2/...` paths. So A = `S0/2`, B = `S0/2948`. `S0/1` holds only `NonPersisted`. | verified |
| Account address | Not found as a field of the account objects (both addresses sit in one shared settings object of type `0x4007`, layout unknown). Rule that works: the address contained in every message of the account's Sent folder (slot +1549). Population check with the live-block rule: 178 of 178 Sent messages of A contain A's address, 104 of 104 of B contain B's address; the other address is in 1 and 11 of them. Account type (Exchange, Outlook.com, IMAP) is not found. | verified (rule), unknown (type) |
| Standard folder slots | The block of an account root starts with an unframed record (about 2 KB before the first framing). It holds references `[type u16][8 zero bytes][ID][u32 2][owner][ID][owner]` at fixed offsets, so **the offset of the slot gives the folder role**. Both accounts use the same offsets. Verified roles: +861 = Inbox (probe mails received in A and B), +1549 = Sent (probe mails sent from A and B). +477 = Drafts (a new unsent `oc-probe-draft` in account A was stored in the +477 folder). +1205 = Outbox, from the move of one sent probe Drafts → Outbox → Sent (account A only). +429 = Deleted Items (a deleted probe moved there). +909 = Junk Email (a probe dragged to Junk moved there). +285 = Archive (a probe archived with the Archive button moved there). Not yet labelled: +621, +1397, +1445 (A only); the UI shows Conversation History in both accounts and Snoozed and RSS Subscriptions only in account A, so one of the two B slots is a folder the UI does not show. | verified (Inbox, Sent, Drafts, Deleted, Junk, Archive; account A, and Inbox and Sent in B), likely (Outbox), unknown (others) |
| Folder records and hierarchy | A parent's folder list (the account root's, or a parent folder's, `0x01e3` and `0x0002` objects) holds one record per child folder: `e3 01`, 8 zero bytes, **parent ID** (u64), a Windows collation sort key (pairs `0e xx`, separators `01`, ending with one `00` byte), the **display name** as UTF-16LE ending with `00 00`, then (form 1) `01`, 4 bytes, `05 00 d8 04`, 4 bytes, `00 00 4d`, 9 zero bytes, **folder ID**, u32 2, account root. Verified: the probe folder name; a user folder of account A holding its six subfolders, matched to the UI by name and by the unread count (the subfolder with the one unread message is `0x1875ab71`). Standard folders store English names (`Inbox`, `Drafts`, `Sent Items`, `Deleted Items`, `Archive`, `Junk Email`) that agree with the role slots. Form 2 records have the same head but no folder ID after the name (the next object header belongs to a different folder); four empty user folders and some standard ones (Outbox, Conversation History, Snoozed, RSS Subscriptions) were only found in this form or not at all. The older entry format (`4d` + 9 zero bytes + ID ... `0x80000000|len` name) gives ID and name for a few of them (one empty user folder of account B is `0x1875ac04`). Every folder that holds live messages in the last snapshot has a form 1 name and parent. Parsing: the record is anchored on its tail; the sort key can contain zero bytes, so the name start is found by walking back from the name terminator while the UTF-16 unit is >= 0x20 (the unit before the name was below 0x20 in every record seen; a sort key that ended with a byte >= 0x20 would add one character to the name). The store keeps old versions of these records, so a version from a block in the live-block index wins, and folders whose indexed versions disagree are reported (0 in the last snapshot). | verified (form 1, on folders with messages), unknown (form 2 IDs) |
| Favorites list | Type `0x0163` of the second per-account object holds an ordered list of folder references (Inbox, Sent, then other folders). It differs per account, so it is a user list (probably Favorites), not a role table. | likely |
| Other types | `0x168`, `0x2f6`, `0xce`, `0xcd`, `0xcc`, `0x154` (about 3,700 each), `0x04` (one new `0x04` object appeared when a message was moved). Meaning unknown. | unknown |

## Message fields (`0xc7`; conversation-form offsets, items are 10 bytes lower)

Offsets are from the start of the object framing. The probes are 2 to 4 messages from one sender; the population checks cover all 2,433 message-form IDs.

| Field | Location and encoding | Confidence |
|---|---|---|
| Version stamp | 6 bytes at +370, little-endian. **It does not change when a message is marked read**: after the user marked about 20 probe copies read, 19 IDs had copies with the same highest stamp and different read bits. So "highest stamp" alone does not find the current copy. **Current copy = the copy whose block is in the live-block index** (see Container). 848 IDs have stamp 0 in every copy. The value is not unique per write; successive values differ by about N x 10^9. | verified (population and probes) |
| Other layout | 11 to 12 IDs pass the message-form test (ID at +32, length >= 1800) but have no folder ID at +1166, and no known folder ID within +1166 ±128. They are a different record layout, not a shifted field. The reader must skip them (counted in `skipped_records`) until the layout is known. They caused 7 of the earlier "8 unread". | verified |
| Status byte | Conversation +1772, item +1762. Bit `0x02` = read: set by "mark read" and cleared by "mark unread" on probes 1, 5 and the reply (item and conversation both change, and the stamp changes too). Bit `0x80` probably marks Focused Inbox (UNVERIFIED). **Population check against the UI (counting items, live rule below): Inbox of account A = 2 unread, as in the UI.** Counting conversations gave 5 and the earlier loose rule 7. One open difference: the UI showed 1 unread in Junk Email, the store has no live item there (see Live-block index). | verified (read bit, Inbox unread), open (Junk) |
| Current folder | u64 at +1166. Changed from `0x18757bbd` to the new folder `0x1880955b` when probe 5 was moved. A sent probe moved Drafts → Outbox → Sent (`0x187578c4` → `0x187578b9` → `0x18757dbd`). Live copies use 16 folder IDs, all of them real folders. | verified |
| Account root | u64 at +32 (`0x18757ba5` for probe 5). | likely |
| Flag status | Byte with values 0 = none, 2 = flagged (matches the Exchange FlagStatus enum; 1 = complete is probable). At +1742 on the probes. Across the store the byte at +1742 is often `0xb7` or `0x85`, so the flag position depends on the layout. | verified on probe (value), unknown (generic location) |
| Strings | Sender address, sender display name, `IPM.Note` item class, Message-ID, preview or body, subject twice (`NormalizedSubject`, `Topic`). | verified (order on probes) |
| HTML body | Small bodies are stored inline as UTF-8 HTML inside the object; large bodies (>= 64 KB) are in `EFMData`. | verified (both cases seen) |
| Dates | .NET ticks (100 ns since 0001-01-01 UTC), u64 little-endian. Message form (parent form: 10 bytes lower): **sent time at +986** (same value in the sender's and the recipient's copy), **received time at +546** (differs per mailbox: the Cc copy and the To copy arrived seconds apart), last-modified time at +530 (has milliseconds), unknown time at +634. The draft creation time appears later in the larger sent-item object. | verified on probe 6 (sent, received), likely (+530) |
| Item class strings | 15,877 `IPM.Note` strings, about 8,700 meeting-item strings, 93 out-of-office templates, 1 NDR. These count string occurrences over all versions and forms, not messages. | verified (as string counts) |
| Recipients | Not in the message object. The parent-form ID owns one object per list: **`0xcc` = To, `0xcd` = Cc, `0xce` = Bcc**, each holding addresses after a sort key. Probe 6 (To B, Cc A) and probe 5 (To A, Cc B) match. Population: `0xcc` is empty for 322 of 3,940 IDs, `0xcd` for 3,056, `0xce` for 3,905 (Bcc appears only on own sent mail). Types `0x154` (always empty) and `0x2f6` (always 181 bytes) come in the same group; meaning unknown. | verified (To, Cc), likely (Bcc) |
| Importance | Byte at +1466 (message form) / +1456 (parent form): 1 for 97% of messages, 2 on the probe sent with High importance. Matches the Exchange enum (0 low, 1 normal, 2 high). | verified on probe 6 (high), likely (low = 0) |
| Thread and reply link | Item: **conversation ID = u64 at +436** (an ID of the conversation form). A reply item holds the ID of the item it answers at +1044. Internet headers are not stored (spec). | verified on the reply probe |
| Attachments | **Has-attachments flag: bit `0x40` of the byte at +1768** (message form) / +1758 (parent form); set on both probes with a file attachment, clear on the others; 6% of parent forms have it. Each attachment is an object of type `0x168` owned by the parent-form ID: a sort key, then the file name as UTF-16LE (probe 6: `oc-probe-att.txt`), then more strings. A u32 equal to the file size (20 bytes) sits at +670 of that object (one probe). Content type not found. `0x168` objects also exist for inline images, so the flag, not the object count, says whether a message has attachments. | verified (flag, name), likely (size), unknown (content type) |
| Link to `EFMData` file | The message's own block (block key = message ID) holds the absolute path of its body files as UTF-16 text, `.../Files/S0/<n>/EFMData/<number>.dat`, outside the object framings. Each large probe had two files (numbers in a row). The account B root `0x02e0` object lists the EFMData paths of that account. The reader should keep only the file name and resolve it under the account's `Files/S0/<n>/EFMData`. Which of the two files is the body shown in the UI is not checked. | verified on probes |

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
- `namefind` and `addrcount` take a real name or address only from the environment variable `HXD_MATCH` (not a flag, so it stays out of shell history and the process list); `tree` marks rows that equal it. They print only IDs, types, offsets, counts, a validated prefix kind and "equals" markers, never the text or the raw bytes next to it. `tree` prints a folder name only when it is a schema word or a strict probe name (`oc-probe-[a-z0-9-]+`). `fieldcmp` masks every u16 that decodes as a letter or digit in any script.
- Two commands written during this stage were removed after review because they could print real text: `keys` (words common to many objects, which include names and addresses) and `head` (raw payload dump, where the redactor misses CJK text).
- Three incidents during discovery, all fixed in the tool: item-class strings glued to a following name field printed about 150 display names; a hex probe printed part of the user's own name as UTF-16 hex; a misparsed folder entry printed the user's own display name next to probe text.

## Open questions

1. Generic field location inside `0xc7` (flag, dates, recipients): walk the property layout instead of fixed offsets.
2. Type-16 blocks: confirm they are freed space (for example, a block becomes type 16 right after a delete).
3. Roles of slots +621, +1397, +1445 (Conversation History, Snoozed and one folder the UI does not show); folder IDs of form 2 records (only empty folders so far); the reader rule for changes made after the last index checkpoint, folder hierarchy, unread and total counts per folder. Layout of the live-block index entries, and which of areas A and B is current.
4. Account type (Exchange, Outlook.com, IMAP) and the layout of the shared settings object `0x4007`.
5. To/Cc lists, importance, attachment metadata, thread/conversation ID, date offsets.
6. Which of the two `EFMData` files of a message holds the displayed body.
7. Deleted items: what a delete changes (folder reference to Deleted Items, or more).
8. Meaning of status bits `0x80` and `0x10`, and of the new `0x04` object after a move.
