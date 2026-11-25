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
| Versions | The store keeps old versions of each object. A key can appear in 10 or more valid blocks. CRC validity does not mean "current"; see the version stamp below. | verified |

## Objects

| Item | Finding | Confidence |
|---|---|---|
| Framing | `06 00 70 05`, u64 object ID, u16 type, u32 length, then fields. Next comes `90 02`, the ID again, a u16 class (`0x4d` = folder) and the owner ID. 49,750 framings found, all lengths fit inside their payload. | verified |
| Payload header | 16 bytes before the first object, for example `00000000 00000000 01000000 <len-16>`. A second header style (`02000000 ...`) holds several objects. | likely |
| Object IDs | u64, values around `0x1875xxxx` to `0x1880xxxx`; new objects get higher IDs. References to other objects are raw u64 IDs. | verified |
| Strings | Length-prefixed: u32 byte length including the terminating NUL, OR `0x80000000`, then 2 bytes, then UTF-16LE text. This is why fields often have no visible separator. Seen on folder names. | verified (folder names), likely (all strings) |
| Message object | Type `0xc7`, in two forms. **Message form**: an ID-range reference at +32, the status byte at +1772, the stamp at +370. **Parent form**: 10 bytes shorter, shares the conversation-level fields, ID of the owner (probe 5: message `0x18809236`, parent `0x18809225`). The message-form ID stayed the same over all versions of probe 5 (read, flag, move). 2,433 message-form IDs in the last snapshot. | verified on probes |
| Folder objects | Several small objects per folder ID (types `0x3d`, `0x1e3`, `0x1cc`) plus a large `0xd6` object (probably the message list), all with class `0x4d` and the account root as owner. | likely |
| Folder names | Only in the owner's folder list (types `0x0002`, `0x01e3`), as entries `4d` + 9 zero bytes, folder ID, `2`, owner, folder ID, owner, 4 zero bytes, length-prefixed name. Only user-created folders have a name entry; standard folders (Inbox and others) have none, and English names are absent (`Sent Items` 0 hits). The field that marks a standard folder type is not found. | verified (user folders), unknown (standard folders) |
| Account roots | `0x18757ba5` owns the folders of one account (the probe folder's owner), `0x18757896` the other. `0x1875791b` and `0x18757694` are referenced next to them. Mapping to `Files/S0/<n>` and to the account address is open. | likely |
| Other types | `0x168`, `0x2f6`, `0xce`, `0xcd`, `0xcc`, `0x154` (about 3,700 each), `0x04` (one new `0x04` object appeared when a message was moved). Meaning unknown. | unknown |

## Message object (`0xc7`, message form) fields

Offsets are from the start of the object framing. The probes are 2 to 4 messages from one sender; the population checks cover all 2,433 message-form IDs.

| Field | Location and encoding | Confidence |
|---|---|---|
| Version stamp | 6 bytes at +370, little-endian. **Current copy = the message-form copy with the highest stamp.** Across the store only 1 of 2,433 IDs has copies with the same highest stamp but different status or folder. 848 IDs have stamp 0 in every copy; their copies agree. The value is not unique per write (one value is shared by many objects); successive values differ by about N x 10^9. | verified (population and probes) |
| Status byte | +1772. Bit `0x02` = read: set by "mark read" and cleared by "mark unread" on probe 1, set by "mark read" on probe 5. Bit `0x80` is set on Inbox copies, absent on the sent copy, and was cleared when probe 5 was moved to a user folder, so it is **not** a delete marker; probably Focused Inbox (UNVERIFIED). Bit `0x10` unknown. Current copies: 8 unread of 2,433 (7 of them have an unusual layout). Unexplained: probe 1 showed `0x82` (read) in the first snapshot before any action. | verified (read bit), unknown (`0x80`, `0x10`) |
| Current folder | u64 at +1166. Changed from `0x18757bbd` to the new folder `0x1880955b` when probe 5 was moved. 27 distinct values in current copies; a few are not IDs (unusual layout). | verified |
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
- Three incidents during discovery, all fixed in the tool: item-class strings glued to a following name field printed about 150 display names; a hex probe printed part of the user's own name as UTF-16 hex; a misparsed folder entry printed the user's own display name next to probe text.

## Open questions

1. Generic field location inside `0xc7` (flag, dates, recipients): walk the property layout instead of fixed offsets.
2. Type-16 block format.
3. Standard folder types (Inbox, Sent, Drafts, Deleted, Archive, Junk), folder hierarchy, unread and total counts per folder.
4. Account mapping: account roots to `Files/S0/<n>` and to the account email.
5. To/Cc lists, importance, attachment metadata, thread/conversation ID, date offsets.
6. Link from message to `EFMData` file.
7. Population check of the read bit against the Outlook unread count (the count did not reach the session).
8. Deleted items: what a delete changes (folder reference to Deleted Items, or more).
9. Meaning of status bits `0x80` and `0x10`, and of the new `0x04` object after a move.
