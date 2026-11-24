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
| Key at +0x20 | Equals the ID in the first object framing of the payload (5,345 of 5,345 blocks whose payload starts with a framing). The spec lists this field as unresolved. Whether that ID is the message identity is open (see Objects). | verified |
| Validity | First snapshot: 17,682 candidates, 17,639 valid, 43 invalid; identical across plain copies and APFS clones, so not caused by copying. Last snapshot: 138 invalid, of which 130 are **type 16 with a payload CRC mismatch** and 8 are type 8 with a header CRC mismatch. Type 16 is a second block format that the reader does not decode yet (no type-16 block has passed the payload check). Until it is decoded, a reader that counts these as damage reports `partial = true` on every call. | verified (counts), unknown (type 16 format) |
| Versions | The store keeps old versions of each object. A key can appear in 10 or more valid blocks. CRC validity does not mean "current". | verified |

## Objects

| Item | Finding | Confidence |
|---|---|---|
| Framing | `06 00 70 05`, u64 object ID, u16 type, u32 length, then fields. 49,750 framings found, all lengths fit inside their payload. | verified |
| Payload header | 16 bytes before the first object, for example `00000000 00000000 01000000 <len-16>`. A second header style (`02000000 ...`) holds several objects. | likely |
| Object IDs | u64, values around `0x1875xxxx` to `0x1880xxxx`; new objects get higher IDs. References to other objects appear as raw u64 IDs. The ID in a framing is the owner or block key: a message can be embedded under another object's framing (probe 1 appeared under its own ID `0x18806590` and under `0x1880657e`, with framing lengths `0xaa0` and `0xa96`). 6,049 distinct framing IDs for 6,245 `0xc7` copies, so framing ID is **not** a stable message identity. | verified |
| Message object | Type `0xc7`. 6,245 framings including old versions. The real message identity (the ID that stays the same across versions) is not located; candidates are the `f0 01 <u64>` references after the strings. | verified (type), unknown (identity) |
| Folder object | Holds the folder name (UTF-16LE) and the parent folder ID. Probe folder `oc-probe-folder` was created as `0x1880955b` with parent `0x18757ba5`. Type code not yet confirmed. | likely |
| Inbox | `0x18757ba5` is referenced by every probe message in the Inbox and is the parent of the probe folder. Its record has no name: well-known folders have no English names in the store (`Sent Items` 0 hits, `Drafts` 0 hits). | likely |
| Other types | `0x168`, `0x2f6`, `0xce`, `0xcd`, `0xcc`, `0x154` (about 3,700 each), `0x04` (a new `0x04` object appeared when a message was moved). Meaning unknown. | unknown |

## Message object (`0xc7`) fields

Offsets are from the start of the object framing in the standalone form. They were confirmed on two probe messages from the same sender only.

| Field | Location and encoding | Confidence |
|---|---|---|
| Version stamp | 6 bytes at +370, little-endian. Never decreased in 10 snapshots, and on the probes the copy with the highest stamp always showed the state the user had just set. But it is **not** a unique per-write version: a new block can carry an older stamp, one value is shared by many objects, and across the store 849 of 6,049 framing IDs have copies with the same highest stamp but different status or flag bytes. Successive values differ by about N x 10^9 (structure unknown). A tie-break is still needed; file offset is not it. | likely (ordering), unknown (current-copy rule) |
| Status byte | +1772 on the probes. Bit `0x02` = read: set by "mark read" and cleared by "mark unread" on probe 1, set by "mark read" on probe 5 (both probes were in the same Inbox `0x18757ba5`). Bit `0x80` = live in its folder (cleared on all copies when probe 5 was moved). Bit `0x10` unknown. Unexplained: probe 1 showed `0x82` (read) in the first snapshot before any action, then `0x80`. The population histogram (`0x80` 3,758, `0x82` 1,686, `0x02` 685, other 116) mixes embedded and standalone forms and old versions, so it does not yet tell how many messages are unread. | verified on probes (read), likely (live bit) |
| Flag status | Byte with values 0 = none, 2 = flagged (matches the Exchange FlagStatus enum; 1 = complete is probable). At +1742 on the probes. Across the store the byte at +1742 is often `0xb7` or `0x85` (2,534 + 1,180, close to the 3,715 count of types `0xcc`/`0xcd`/`0xce`), which suggests a 10-byte shift in the embedded form, not checked. | verified on probe (value), unknown (generic location) |
| Strings | Sender address, sender display name, `IPM.Note` item class, Message-ID, preview or body, subject twice (`NormalizedSubject`, `Topic`). UTF-16LE. Often **not** NUL-separated: the next field can follow the item class directly. | verified (order on probes) |
| HTML body | Small bodies are stored inline as UTF-8 HTML inside the object; large bodies (>= 64 KB) are in `EFMData`. | verified (both cases seen) |
| Dates | .NET ticks (100 ns since 0001-01-01 UTC), u64 little-endian, several per object (sent, received, modified). Which offset is which is not mapped yet. | verified (encoding), unknown (offsets) |
| Folder reference | The object references `0x18757ba5` (Inbox) after a type `0xbf` marker. | likely |
| Item classes | 15,877 `IPM.Note`, about 8,700 meeting items, 93 out-of-office templates, 1 NDR. | verified |
| To/Cc split, importance, attachments metadata, thread ID, internet headers | Not mapped. The Osa logs show that the protocol carries `To`, `Cc`, `Importance`, `HasAttachment`, `ThreadId`, `ConversationId`. Internet headers are not stored (spec). | unknown |
| Attachments | The attachment file name (UTF-16LE) is stored in its own object near the message. Size, content type and the link to the message are not mapped. | likely (name), unknown (rest) |
| Link to `EFMData` file | Not found as u32 or text in payloads that contain the message subject. A search of all payloads found the file numbers in type `0x2cb` at +104 and +112 and in type `0x2e0`; not followed up. | unknown |

## Snapshot method

| Method | Result | Confidence |
|---|---|---|
| Streaming copy (`os.Open` read-only, copy to `os.MkdirTemp` 0700, retry when size or mtime changes) | Consistent: same block and damage counts as the clone. About 50 ms for 117 MB when the file is in the page cache. | verified |
| APFS clone (`cp -c`) | Same result, 30 ms. Needs `clonefile(2)`; not in the Go standard library. | verified |
| Decision | Streaming copy. No extra dependency; the per-block CRCs detect a block that Outlook rewrites during the copy. | decision |

The live file changed between snapshots taken seconds apart, even with no user action (a stamp field changed in every copy).

## Body coverage

| Tier | Finding | Confidence |
|---|---|---|
| Preview only | About 255 characters (spec: 89% of messages on the reference store). | not measured here |
| Inline HTML | Present for small bodies the client downloaded. | verified on probes |
| `EFMData` | 2,128 files before the experiment; all bodies >= 64 KB. | verified |

## Privacy notes for development

- `hxdiscover` (dev tool, not shipped) prints only counts, offsets, hex of binary fields, and redacted shapes of text. It prints text only when it contains the synthetic `oc-probe` marker or is a known schema word.
- Two incidents during discovery, both fixed in the tool: item-class strings glued to a following name field printed about 150 display names; a hex probe printed part of the user's own name as UTF-16 hex.

## Open questions

1. Message identity and the current-copy rule (blocking for read state, flags, folders and deduplication).
2. Generic field location inside `0xc7`: standalone vs embedded form, then walk the property layout instead of fixed offsets.
3. Type-16 block format.
4. Which object is the moved message: the new type `0x04` object, or a later rewrite of the `0xc7` object after sync.
5. Folder object type, folder names, well-known folder type enum (`Inbox`, `Sent`, `Drafts`, `Deleted`, `Archive`, `Junk`), unread and total counts.
6. Account mapping: object or setting that ties objects to `Files/S0/2` and `Files/S0/2948` and to the account email.
7. To/Cc lists, importance, attachment metadata, thread/conversation ID, date offsets.
8. Link from message to `EFMData` file.
9. Population check of the read bit against the Outlook unread count (the count did not reach the session).
10. Deleted items: is delete the same as move (live bit cleared plus new object in Deleted Items)?
