package pack

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// Reader is an open pack. Open validates the header and index eagerly —
// magic, format version, size limits, bounds, overlaps, and duplicate keys —
// without decoding any record; record bytes are only read on demand through
// ReadAt. Methods are safe for concurrent use once NewReader returns.
type Reader struct {
	src            io.ReaderAt
	size           int64
	closer         io.Closer
	content        ContentType
	buildID        string
	weigherVersion uint32
	recordCodec    string
	records        []Record
	byKey          map[string]int
}

// Open opens the pack file at path and validates it as content.
func Open(path string, content ContentType) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	reader, err := NewReader(file, info.Size(), content)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	reader.closer = file
	return reader, nil
}

// NewReader validates a pack held by any ReaderAt (a file, an mmap, or a
// byte slice via bytes.NewReader). The reader does not own src; Close is a
// no-op unless the reader came from Open.
func NewReader(src io.ReaderAt, size int64, content ContentType) (*Reader, error) {
	if !content.valid() {
		return nil, fmt.Errorf("pack: invalid content type %d", content)
	}
	section := io.NewSectionReader(src, 0, size)

	var magic [magicLen]byte
	if err := readFull(section, magic[:]); err != nil {
		return nil, err
	}
	if magic != content.magic() {
		for _, other := range []ContentType{ContentPlans, ContentStatic} {
			if other != content && magic == other.magic() {
				return nil, fmt.Errorf("pack: contains %s, expected %s", other, content)
			}
		}
		return nil, fmt.Errorf("pack: bad magic %q", magic)
	}
	formatVersion, err := readUint32(section)
	if err != nil {
		return nil, err
	}
	if formatVersion != FormatVersion {
		return nil, fmt.Errorf("pack: unsupported format version %d, want %d", formatVersion, FormatVersion)
	}
	weigherVersion, err := readUint32(section)
	if err != nil {
		return nil, err
	}
	buildID, err := readString(section, MaxBuildIDLen, "build ID")
	if err != nil {
		return nil, err
	}
	codec, err := readString(section, MaxBuildIDLen, "record codec")
	if err != nil {
		return nil, err
	}
	if codec != RecordCodecJSONZstd {
		return nil, fmt.Errorf("pack: unsupported record codec %q, want %q", codec, RecordCodecJSONZstd)
	}
	recordCount, err := readUint32(section)
	if err != nil {
		return nil, err
	}
	if recordCount > MaxRecords {
		return nil, fmt.Errorf("pack: record count %d exceeds %d", recordCount, MaxRecords)
	}
	indexLen, err := readUint64(section)
	if err != nil {
		return nil, err
	}
	headerEnd := uint64(headerLen(buildID, codec))
	if indexLen > uint64(size)-headerEnd {
		return nil, fmt.Errorf("pack: index length %d exceeds file size %d", indexLen, size)
	}
	recordsStart := headerEnd + indexLen

	index := make([]byte, indexLen)
	if err := readFull(section, index); err != nil {
		return nil, err
	}
	records := make([]Record, 0, recordCount)
	byKey := make(map[string]int, recordCount)
	for i := range int(recordCount) {
		rec, rest, err := parseIndexEntry(index)
		if err != nil {
			return nil, fmt.Errorf("pack: index entry %d: %w", i, err)
		}
		index = rest
		if prev := len(records) - 1; prev >= 0 {
			switch strings.Compare(records[prev].Key, rec.Key) {
			case 0:
				return nil, fmt.Errorf("pack: duplicate key %q", rec.Key)
			case 1:
				return nil, fmt.Errorf("pack: index is not sorted at key %q", rec.Key)
			}
		}
		if rec.Offset < recordsStart || rec.Offset > uint64(size) || rec.Length > uint64(size)-rec.Offset {
			return nil, fmt.Errorf("pack: record %q at [%d,%d) is outside the record region [%d,%d)", rec.Key, rec.Offset, rec.Offset+rec.Length, recordsStart, size)
		}
		byKey[rec.Key] = len(records)
		records = append(records, rec)
	}
	if len(index) != 0 {
		return nil, fmt.Errorf("pack: index has %d trailing bytes after %d entries", len(index), recordCount)
	}

	spans := slices.Clone(records)
	slices.SortFunc(spans, func(a, b Record) int {
		if a.Offset != b.Offset {
			if a.Offset < b.Offset {
				return -1
			}
			return 1
		}
		return 0
	})
	for i := 1; i < len(spans); i++ {
		if prev := spans[i-1]; prev.Offset+prev.Length > spans[i].Offset {
			return nil, fmt.Errorf("pack: records %q and %q overlap", prev.Key, spans[i].Key)
		}
	}

	if weigherVersion != WeigherVersion {
		for i := range records {
			records[i].DecodedWeight, records[i].PeakWeight = FallbackWeights(content, records[i].EncodedLen)
		}
	}
	return &Reader{
		src:            src,
		size:           size,
		content:        content,
		buildID:        buildID,
		weigherVersion: weigherVersion,
		recordCodec:    codec,
		records:        records,
		byKey:          byKey,
	}, nil
}

// Close releases the underlying file when the reader came from Open.
func (r *Reader) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func (r *Reader) Content() ContentType { return r.content }

func (r *Reader) BuildID() string { return r.buildID }

// WeigherVersion is the weight model the pack was written with. When it is
// not this package's WeigherVersion, record weights have already been
// replaced with FallbackWeights.
func (r *Reader) WeigherVersion() uint32 { return r.weigherVersion }

func (r *Reader) RecordCodec() string { return r.recordCodec }

func (r *Reader) Len() int { return len(r.records) }

func (r *Reader) Has(key string) bool {
	_, ok := r.byKey[key]
	return ok
}

// Record returns the index entry for key.
func (r *Reader) Record(key string) (Record, bool) {
	i, ok := r.byKey[key]
	if !ok {
		return Record{}, false
	}
	return r.records[i], true
}

// Records returns a copy of the index, sorted ascending by key.
func (r *Reader) Records() []Record { return slices.Clone(r.records) }

// ReadRecord returns the raw stored (zstd-compressed) bytes for key after
// verifying them against the index digest.
func (r *Reader) ReadRecord(key string) ([]byte, error) {
	rec, ok := r.Record(key)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	stored := make([]byte, rec.Length)
	if err := readFull(io.NewSectionReader(r.src, int64(rec.Offset), int64(rec.Length)), stored); err != nil {
		return nil, fmt.Errorf("pack: record %q: %w", key, err)
	}
	if sha256.Sum256(stored) != rec.Digest {
		return nil, fmt.Errorf("%w: %q", ErrDigestMismatch, key)
	}
	return stored, nil
}

// DecodeJSONRecord reads, digest-verifies, and decompresses the record for
// key, returning its JSON bytes. Callers hand those to renderplan.Parse or
// the static entry decoder.
func (r *Reader) DecodeJSONRecord(key string) ([]byte, error) {
	rec, ok := r.Record(key)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	stored, err := r.ReadRecord(key)
	if err != nil {
		return nil, err
	}
	decoder, err := sharedDecoder()
	if err != nil {
		return nil, err
	}
	encoded, err := decoder.DecodeAll(stored, make([]byte, 0, rec.EncodedLen))
	if err != nil {
		return nil, fmt.Errorf("pack: record %q: %w", key, err)
	}
	if uint64(len(encoded)) != rec.EncodedLen {
		return nil, fmt.Errorf("pack: record %q: decoded length %d does not match indexed encoded length %d", key, len(encoded), rec.EncodedLen)
	}
	return encoded, nil
}

func parseIndexEntry(index []byte) (Record, []byte, error) {
	if len(index) < 2 {
		return Record{}, nil, fmt.Errorf("truncated index")
	}
	keyLen := int(binary.BigEndian.Uint16(index))
	if keyLen == 0 || keyLen > MaxKeyLen {
		return Record{}, nil, fmt.Errorf("key length %d must be 1..%d", keyLen, MaxKeyLen)
	}
	if len(index) < indexEntryFixed+keyLen {
		return Record{}, nil, fmt.Errorf("truncated index")
	}
	rec := Record{Key: string(index[2 : 2+keyLen])}
	fields := index[2+keyLen:]
	rec.Offset = binary.BigEndian.Uint64(fields)
	rec.Length = binary.BigEndian.Uint64(fields[8:])
	rec.EncodedLen = binary.BigEndian.Uint64(fields[16:])
	rec.DecodedWeight = binary.BigEndian.Uint64(fields[24:])
	rec.PeakWeight = binary.BigEndian.Uint64(fields[32:])
	rec.Digest = [sha256.Size]byte(fields[40 : 40+sha256.Size])
	if rec.Length == 0 || rec.Length > MaxRecordStoredLen {
		return Record{}, nil, fmt.Errorf("record %q: stored length %d must be 1..%d", rec.Key, rec.Length, MaxRecordStoredLen)
	}
	if rec.EncodedLen == 0 || rec.EncodedLen > MaxRecordEncodedLen {
		return Record{}, nil, fmt.Errorf("record %q: encoded length %d must be 1..%d", rec.Key, rec.EncodedLen, MaxRecordEncodedLen)
	}
	return rec, index[indexEntryFixed+keyLen:], nil
}

func readString(r io.Reader, maxLen int, what string) (string, error) {
	length, err := readUint16(r)
	if err != nil {
		return "", err
	}
	if length == 0 || int(length) > maxLen {
		return "", fmt.Errorf("pack: %s length %d must be 1..%d", what, length, maxLen)
	}
	var value strings.Builder
	if _, err := io.CopyN(&value, r, int64(length)); err != nil {
		return "", truncated(err)
	}
	return value.String(), nil
}

func readUint16(r io.Reader) (uint16, error) {
	var buf [2]byte
	if err := readFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(buf[:]), nil
}

func readUint32(r io.Reader) (uint32, error) {
	var buf [4]byte
	if err := readFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func readUint64(r io.Reader) (uint64, error) {
	var buf [8]byte
	if err := readFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(buf[:]), nil
}

func readFull(r io.Reader, buf []byte) error {
	if _, err := io.ReadFull(r, buf); err != nil {
		return truncated(err)
	}
	return nil
}

func truncated(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return fmt.Errorf("pack: truncated file: %w", err)
	}
	return err
}
