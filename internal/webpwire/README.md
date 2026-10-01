# Shared container cursor boundary

`webpwire` is an internal leaf package. Neither the public `container` package
nor the root pixel package is imported here. Both facades translate `Fault` and
`ResourceError` into their existing public errors. The parser owns RIFF ordering,
nested extents, padding, codec-header probes, metadata declaration checks and
aggregate counters. It never validates entropy coding or composes pixels.

## Read/lifetime contract

- `Open` reads control chunks through the first frame boundary. For ANMF this is
  the fixed 16-byte control header; for an unwrapped still codec it includes the
  5/10-byte codec prefix needed to obtain dimensions. No compressed frame is
  allocated during that operation. Metadata preceding the frame is retained
  according to policy.
- `NextFrame` returns one fully bounded compressed frame. Each codec header is
  checked against its declared length and rectangle before payload allocation.
  There is no enclosing ANMF allocation and no RIFF spool.
- The returned `Frame` owns its payloads and `UnknownChunks`. Cursor retains no
  reference to them. The caller drops all references and then releases exactly
  `Frame.OwnedBytes` from the shared `Working` object. An unsuccessful NextFrame
  releases its partial frame internally.
- Metadata and top-level unknowns remain cursor-owned. Their accessors provide
  borrowed internal views. `Close` drops them and releases their reservations;
  it neither consumes input nor closes the caller reader. Demux moves the byte
  slices into the public File before closing its private cursor.
- Pixel mode retains the first metadata occurrence, including an empty first
  occurrence, and drains duplicates and unknowns. Preservation mode retains all
  occurrences and ordered per-frame and file-level unknowns.
- Successful EOF means the complete declared RIFF extent, including all trailer
  chunks and feature-presence checks, was validated. It does not probe beyond
  that extent. Summary is absent before this point. Errors are sticky; early
  Close does not finalize or validate.

## Budget audit

Root Reader reserves its 64 KiB fixed-state allowance **before** calling Open.
That allowance covers the Cursor object, its 4096-byte drain buffer, temporary
headers, one pending-frame descriptor, short-lived FourCC strings and bounded
error state. It is separate from the allocations below. Container Demux has no
public working-memory contract; its payload and count budgets still apply.

Every retained payload reserves its declared byte length before `make` and
releases it if reading or padding fails. Prefix bytes are copied directly into
that allocation. Skipped chunks use fixed scratch and reserve no payload bytes.

Retained Chunk slices use explicitly managed doubling rather than Go's implicit
append growth. A conservative 64-byte capacity slot covers the Chunk backing
entry and its FourCC on supported 32/64-bit targets. Growth reserves the complete
new backing before allocating/copying, then releases the old backing. The exact
reservation carried by Frame.OwnedBytes includes both payload bytes and this
conservative descriptor allowance. Descriptor arithmetic is platform-checked.

The decoder and compositor use the same Working object. Their reservations must
remain live until the corresponding compressed frame, decoded subframe, codec
scratch, canvas or retained document frame is no longer library-owned. Reader's
Metadata copies must reserve allocation overlap, then relinquish that copy's
reservation when ownership transfers to the caller. Cursor.Close only releases
cursor-owned storage; it does not release external canvas/frame reservations.

All outer and nested chunk headers share one count budget. All encountered
metadata payload bytes count, including skipped duplicates. Input counts the
one declared RIFF extent. For container retained-byte compatibility, each outer
payload counts once: an ANMF includes its 16-byte control and nested framing,
and children are not charged again. This payload allowance is distinct from
live Working reservations.

Decoded-pixel and duration counters are cumulative over stored frames once;
loop count never multiplies work. Duration aggregation stays in checked integer
milliseconds. Root's normalized MaxDuration admission precedes any conversion
of the aggregate to time.Duration; the container-only summary keeps milliseconds.

These bounds cover library-managed backing storage, not Go runtime/allocator
metadata, caller-retained storage, garbage awaiting collection, process RSS or
blocked caller I/O. Context checks occur before and after every Read and during
all bounded skip/read loops. No cancellation goroutines are created.
