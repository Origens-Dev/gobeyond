// Package pack implements the immutable binary container that carries
// GoBeyond's pack-only runtime artifacts: render plans (.gbp) and packaged
// static entries (.gbs). See docs/adr/004-lazy-route-residency.md.
//
// Both files share one container layout — a fixed header, a sorted index, and
// per-record zstd-compressed JSON — distinguished by the magic bytes. Opening
// a pack validates the header and index without decoding any record; records
// are read on demand through ReadAt so residency stays bounded.
package pack

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// FormatVersion is the container format implemented by this package.
const FormatVersion = 1

// RecordCodecJSONZstd is record codec v1: each record is one zstd-compressed
// JSON document. Whole-file compression is rejected by design because it
// breaks random access.
const RecordCodecJSONZstd = "json+zstd"

// Conventional file extensions for the two pack content types.
const (
	ExtPlans  = ".gbp"
	ExtStatic = ".gbs"
)

// Capability identifiers a build publishes in its deploy metadata
// (compatibility.json / artifacts.json) to declare the pack artifacts it
// emitted. Hosting and tooling key off these strings, not file extensions.
const (
	PlanPackCapability   = "gobeyond.plan-pack/v1"
	StaticPackCapability = "gobeyond.static-pack/v1"
)

// Size limits enforced when opening a pack, before any record is decoded.
const (
	MaxBuildIDLen       = 256
	MaxKeyLen           = 1024
	MaxRecords          = 1 << 20
	MaxRecordStoredLen  = 1 << 30 // compressed record bytes on disk
	MaxRecordEncodedLen = 1 << 30 // JSON bytes after decompression
)

var (
	// ErrNotFound reports a key absent from the pack index.
	ErrNotFound = errors.New("pack: record not found")
	// ErrDigestMismatch reports stored record bytes that do not match the
	// index digest. This is an immutable integrity failure.
	ErrDigestMismatch = errors.New("pack: record digest mismatch")
)

// ContentType selects which artifact a pack carries.
type ContentType uint8

const (
	// ContentPlans is a render-plan pack (.gbp); keys are route IDs.
	ContentPlans ContentType = iota + 1
	// ContentStatic is a packaged static-entry pack (.gbs); keys are
	// StaticEntryKey values.
	ContentStatic
)

func (c ContentType) String() string {
	switch c {
	case ContentPlans:
		return "render plans"
	case ContentStatic:
		return "static entries"
	default:
		return "unknown"
	}
}

func (c ContentType) valid() bool { return c == ContentPlans || c == ContentStatic }

const magicLen = 8

// PNG-style magics: a high bit to catch text-mode mangling, CRLF and LF to
// catch newline translation, and 0x1a to stop accidental terminal dumps.
var (
	magicPlans  = [magicLen]byte{0x89, 'G', 'B', 'P', '\r', '\n', 0x1a, '\n'}
	magicStatic = [magicLen]byte{0x89, 'G', 'B', 'S', '\r', '\n', 0x1a, '\n'}
)

func (c ContentType) magic() [magicLen]byte {
	if c == ContentStatic {
		return magicStatic
	}
	return magicPlans
}

// Record describes one entry in a pack's sorted index.
type Record struct {
	Key           string
	Offset        uint64            // absolute file offset of the stored bytes
	Length        uint64            // stored (compressed) length in bytes
	Digest        [sha256.Size]byte // SHA-256 of the stored bytes
	EncodedLen    uint64            // JSON length in bytes before compression
	DecodedWeight uint64            // estimated resident weight once decoded
	PeakWeight    uint64            // estimated peak weight while decoding
}

// Container byte layout (all integers big-endian):
//
//	magic[8] formatVersion:u32 weigherVersion:u32
//	buildIDLen:u16 buildID codecLen:u16 codec
//	recordCount:u32 indexLen:u64
//	index entries, sorted ascending by key:
//	  keyLen:u16 key offset:u64 length:u64 encodedLen:u64
//	  decodedWeight:u64 peakWeight:u64 digest[32]
//	record bytes
const (
	offFormatVersion  = magicLen
	offWeigherVersion = offFormatVersion + 4
	offBuildIDLen     = offWeigherVersion + 4
	indexEntryFixed   = 2 + 5*8 + sha256.Size
)

func headerLen(buildID, codec string) int {
	return offBuildIDLen + 2 + len(buildID) + 2 + len(codec) + 4 + 8
}

func indexEntryLen(key string) int { return indexEntryFixed + len(key) }

func appendHeader(dst []byte, magic [magicLen]byte, formatVersion, weigherVersion uint32, buildID, codec string, recordCount uint32, indexLen uint64) []byte {
	dst = append(dst, magic[:]...)
	dst = binary.BigEndian.AppendUint32(dst, formatVersion)
	dst = binary.BigEndian.AppendUint32(dst, weigherVersion)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(buildID)))
	dst = append(dst, buildID...)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(codec)))
	dst = append(dst, codec...)
	dst = binary.BigEndian.AppendUint32(dst, recordCount)
	dst = binary.BigEndian.AppendUint64(dst, indexLen)
	return dst
}

func appendIndexEntry(dst []byte, rec Record) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(rec.Key)))
	dst = append(dst, rec.Key...)
	dst = binary.BigEndian.AppendUint64(dst, rec.Offset)
	dst = binary.BigEndian.AppendUint64(dst, rec.Length)
	dst = binary.BigEndian.AppendUint64(dst, rec.EncodedLen)
	dst = binary.BigEndian.AppendUint64(dst, rec.DecodedWeight)
	dst = binary.BigEndian.AppendUint64(dst, rec.PeakWeight)
	dst = append(dst, rec.Digest[:]...)
	return dst
}
