package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

func planJSON(routeID string) []byte {
	return []byte(`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"` + routeID + `","root":{"kind":"element","tag":"main","children":[{"kind":"text","value":{"kind":"literal","value":"` + routeID + ` page"}}]}}`)
}

func buildPack(t *testing.T, content ContentType, buildID string, entries map[string][]byte) []byte {
	t.Helper()
	builder, err := NewBuilder(content, buildID)
	if err != nil {
		t.Fatal(err)
	}
	for key, encoded := range entries {
		if content == ContentPlans {
			err = builder.AddPlan(key, encoded)
		} else {
			err = builder.AddStatic(key, encoded)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := builder.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openPack(t *testing.T, data []byte, content ContentType) *Reader {
	t.Helper()
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)), content)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// forge assembles a container from explicit parts so tests can produce
// invalid indexes the Builder refuses to write.
func forge(content ContentType, weigherVersion uint32, buildID, codec string, recs []Record, tail []byte) []byte {
	indexLen := 0
	for _, rec := range recs {
		indexLen += indexEntryLen(rec.Key)
	}
	data := appendHeader(nil, content.magic(), FormatVersion, weigherVersion, buildID, codec, uint32(len(recs)), uint64(indexLen))
	for _, rec := range recs {
		data = appendIndexEntry(data, rec)
	}
	return append(data, tail...)
}

func compress(t *testing.T, encoded []byte) ([]byte, [sha256.Size]byte) {
	t.Helper()
	encoder, err := sharedEncoder()
	if err != nil {
		t.Fatal(err)
	}
	stored := encoder.EncodeAll(encoded, nil)
	return stored, sha256.Sum256(stored)
}

func wantOpenError(t *testing.T, data []byte, content ContentType, substr string) {
	t.Helper()
	_, err := NewReader(bytes.NewReader(data), int64(len(data)), content)
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got %v", substr, err)
	}
}

func TestPlansRoundTrip(t *testing.T) {
	plans := map[string][]byte{
		"home":          planJSON("home"),
		"products_slug": planJSON("products_slug"),
		"about":         planJSON("about"),
	}
	path := filepath.Join(t.TempDir(), "render-plans"+ExtPlans)
	if err := WritePlans(path, "build-42", plans); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(path, ContentPlans)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if reader.BuildID() != "build-42" || reader.RecordCodec() != RecordCodecJSONZstd || reader.WeigherVersion() != WeigherVersion {
		t.Fatalf("unexpected header: %q %q %d", reader.BuildID(), reader.RecordCodec(), reader.WeigherVersion())
	}
	if reader.Len() != len(plans) {
		t.Fatalf("expected %d records, got %d", len(plans), reader.Len())
	}
	records := reader.Records()
	for i := 1; i < len(records); i++ {
		if records[i-1].Key >= records[i].Key {
			t.Fatalf("index is not sorted: %q >= %q", records[i-1].Key, records[i].Key)
		}
	}
	for routeID, encoded := range plans {
		if !reader.Has(routeID) {
			t.Fatalf("missing %q", routeID)
		}
		rec, ok := reader.Record(routeID)
		if !ok || rec.EncodedLen != uint64(len(encoded)) {
			t.Fatalf("record %q: %+v", routeID, rec)
		}
		plan, err := renderplan.Parse(encoded)
		if err != nil {
			t.Fatal(err)
		}
		wantDecoded, wantPeak := PlanWeights(plan, len(encoded))
		if rec.DecodedWeight != wantDecoded || rec.PeakWeight != wantPeak {
			t.Fatalf("record %q weights (%d, %d), want (%d, %d)", routeID, rec.DecodedWeight, rec.PeakWeight, wantDecoded, wantPeak)
		}
		stored, err := reader.ReadRecord(routeID)
		if err != nil {
			t.Fatal(err)
		}
		if uint64(len(stored)) != rec.Length || sha256.Sum256(stored) != rec.Digest {
			t.Fatalf("stored bytes for %q do not match the index", routeID)
		}
		decoded, err := reader.DecodeJSONRecord(routeID)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, encoded) {
			t.Fatalf("decoded JSON for %q does not round-trip", routeID)
		}
		if _, err := renderplan.Parse(decoded); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.ReadRecord("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := reader.DecodeJSONRecord("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestWritePlansIsDeterministic(t *testing.T) {
	plans := map[string][]byte{"home": planJSON("home"), "about": planJSON("about")}
	dir := t.TempDir()
	first := filepath.Join(dir, "first"+ExtPlans)
	second := filepath.Join(dir, "second"+ExtPlans)
	if err := WritePlans(first, "build-1", plans); err != nil {
		t.Fatal(err)
	}
	if err := WritePlans(second, "build-1", plans); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("pack bytes differ between identical builds")
	}
}

func TestStaticRoundTrip(t *testing.T) {
	entries := map[string][]byte{
		StaticEntryKey("home", nil):                                      []byte(`{"props":{"title":"Home"},"metadata":{"lang":"en","title":"Home"}}`),
		StaticEntryKey("products_slug", map[string]string{"slug": "go"}): []byte(`{"props":{"title":"Go"},"metadata":null}`),
	}
	path := filepath.Join(t.TempDir(), "static-build"+ExtStatic)
	if err := WriteStatic(path, "build-42", entries); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(path, ContentStatic)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Content() != ContentStatic || reader.Len() != len(entries) {
		t.Fatalf("unexpected reader: %v %d", reader.Content(), reader.Len())
	}
	for key, encoded := range entries {
		decoded, err := reader.DecodeJSONRecord(key)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, encoded) {
			t.Fatalf("decoded JSON for %q does not round-trip", key)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		wantDecoded, wantPeak := StaticWeights(value, nil, len(encoded))
		rec, _ := reader.Record(key)
		if rec.DecodedWeight != wantDecoded || rec.PeakWeight != wantPeak {
			t.Fatalf("record %q weights (%d, %d), want (%d, %d)", key, rec.DecodedWeight, rec.PeakWeight, wantDecoded, wantPeak)
		}
	}
}

func TestStaticEntryKey(t *testing.T) {
	cases := []struct {
		routeID string
		params  map[string]string
		want    string
	}{
		{"home", nil, "home?"},
		{"home", map[string]string{}, "home?"},
		{"docs_rest", map[string]string{"rest": ""}, "docs_rest?rest="},
		{"products_slug", map[string]string{"slug": "espresso maker"}, "products_slug?slug=espresso+maker"},
		{"b", map[string]string{"b": "2", "a": "1"}, "b?a=1&b=2"},
		{"x", map[string]string{"a&b": "c=d"}, "x?a%26b=c%3Dd"},
	}
	for _, tc := range cases {
		if got := StaticEntryKey(tc.routeID, tc.params); got != tc.want {
			t.Fatalf("StaticEntryKey(%q, %v) = %q, want %q", tc.routeID, tc.params, got, tc.want)
		}
	}
}

func TestEmptyPack(t *testing.T) {
	data := buildPack(t, ContentPlans, "build-empty", nil)
	reader := openPack(t, data, ContentPlans)
	if reader.Len() != 0 || len(reader.Records()) != 0 {
		t.Fatalf("expected empty pack, got %d records", reader.Len())
	}
	if reader.Has("home") {
		t.Fatal("empty pack claims to have a record")
	}
	if _, err := reader.ReadRecord("home"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	path := filepath.Join(t.TempDir(), "empty"+ExtStatic)
	if err := WriteStatic(path, "build-empty", nil); err != nil {
		t.Fatal(err)
	}
	fromFile, err := Open(path, ContentStatic)
	if err != nil {
		t.Fatal(err)
	}
	defer fromFile.Close()
	if fromFile.Len() != 0 {
		t.Fatalf("expected empty pack, got %d records", fromFile.Len())
	}
}

func TestOpenRejectsWrongContentType(t *testing.T) {
	data := buildPack(t, ContentPlans, "build-1", map[string][]byte{"home": planJSON("home")})
	wantOpenError(t, data, ContentStatic, "contains render plans")
}

func TestOpenRejectsCorruptHeader(t *testing.T) {
	valid := buildPack(t, ContentPlans, "build-1", map[string][]byte{"home": planJSON("home")})

	badMagic := bytes.Clone(valid)
	badMagic[0] ^= 0xff
	wantOpenError(t, badMagic, ContentPlans, "bad magic")

	badVersion := bytes.Clone(valid)
	binary.BigEndian.PutUint32(badVersion[offFormatVersion:], FormatVersion+1)
	wantOpenError(t, badVersion, ContentPlans, "unsupported format version")

	wantOpenError(t, forge(ContentPlans, WeigherVersion, "build-1", "json+gzip", nil, nil), ContentPlans, "unsupported record codec")
	wantOpenError(t, forge(ContentPlans, WeigherVersion, "", RecordCodecJSONZstd, nil, nil), ContentPlans, "build ID length")

	tooManyRecords := appendHeader(nil, magicPlans, FormatVersion, WeigherVersion, "build-1", RecordCodecJSONZstd, MaxRecords+1, 0)
	wantOpenError(t, tooManyRecords, ContentPlans, "record count")

	hugeIndex := appendHeader(nil, magicPlans, FormatVersion, WeigherVersion, "build-1", RecordCodecJSONZstd, 0, 1<<40)
	wantOpenError(t, hugeIndex, ContentPlans, "index length")
}

func TestOpenRejectsTruncatedFile(t *testing.T) {
	valid := buildPack(t, ContentPlans, "build-1", map[string][]byte{"home": planJSON("home")})
	head := headerLen("build-1", RecordCodecJSONZstd)
	for _, size := range []int{0, 4, magicLen, head - 3} {
		wantOpenError(t, valid[:size], ContentPlans, "truncated")
	}
	// Cut inside the index: the header survives, so this reads as an index
	// that no longer fits the file.
	wantOpenError(t, valid[:head+5], ContentPlans, "index length")
}

func TestOpenRejectsDuplicateKeys(t *testing.T) {
	recs := []Record{
		{Key: "home", Offset: 0, Length: 1, EncodedLen: 1},
		{Key: "home", Offset: 1, Length: 1, EncodedLen: 1},
	}
	start := uint64(headerLen("build-1", RecordCodecJSONZstd) + indexEntryLen("home")*2)
	recs[0].Offset, recs[1].Offset = start, start+1
	data := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd, recs, []byte{0, 0})
	wantOpenError(t, data, ContentPlans, "duplicate key")
}

func TestOpenRejectsUnsortedIndex(t *testing.T) {
	start := uint64(headerLen("build-1", RecordCodecJSONZstd) + indexEntryLen("b") + indexEntryLen("a"))
	recs := []Record{
		{Key: "b", Offset: start, Length: 1, EncodedLen: 1},
		{Key: "a", Offset: start + 1, Length: 1, EncodedLen: 1},
	}
	data := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd, recs, []byte{0, 0})
	wantOpenError(t, data, ContentPlans, "not sorted")
}

func TestOpenRejectsOverlappingRecords(t *testing.T) {
	start := uint64(headerLen("build-1", RecordCodecJSONZstd) + indexEntryLen("a") + indexEntryLen("b"))
	recs := []Record{
		{Key: "a", Offset: start, Length: 10, EncodedLen: 10},
		{Key: "b", Offset: start + 5, Length: 10, EncodedLen: 10},
	}
	data := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd, recs, make([]byte, 15))
	wantOpenError(t, data, ContentPlans, "overlap")
}

func TestOpenRejectsOutOfBoundsRecords(t *testing.T) {
	start := uint64(headerLen("build-1", RecordCodecJSONZstd) + indexEntryLen("a"))

	pastEnd := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd,
		[]Record{{Key: "a", Offset: start, Length: 11, EncodedLen: 1}}, make([]byte, 10))
	wantOpenError(t, pastEnd, ContentPlans, "outside the record region")

	insideIndex := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd,
		[]Record{{Key: "a", Offset: start - 1, Length: 2, EncodedLen: 1}}, make([]byte, 10))
	wantOpenError(t, insideIndex, ContentPlans, "outside the record region")

	zeroLength := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd,
		[]Record{{Key: "a", Offset: start, Length: 0, EncodedLen: 1}}, make([]byte, 10))
	wantOpenError(t, zeroLength, ContentPlans, "stored length")
}

func TestReadRecordRejectsDigestMismatch(t *testing.T) {
	data := buildPack(t, ContentPlans, "build-1", map[string][]byte{"home": planJSON("home")})
	reader := openPack(t, data, ContentPlans)
	rec, _ := reader.Record("home")

	corrupt := bytes.Clone(data)
	corrupt[rec.Offset] ^= 0xff
	reader = openPack(t, corrupt, ContentPlans)
	if _, err := reader.ReadRecord("home"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
	if _, err := reader.DecodeJSONRecord("home"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
}

func TestDecodeRejectsEncodedLengthMismatch(t *testing.T) {
	encoded := planJSON("home")
	stored, digest := compress(t, encoded)
	start := uint64(headerLen("build-1", RecordCodecJSONZstd) + indexEntryLen("home"))
	rec := Record{
		Key:        "home",
		Offset:     start,
		Length:     uint64(len(stored)),
		Digest:     digest,
		EncodedLen: uint64(len(encoded)) + 1,
	}
	data := forge(ContentPlans, WeigherVersion, "build-1", RecordCodecJSONZstd, []Record{rec}, stored)
	reader := openPack(t, data, ContentPlans)
	if _, err := reader.ReadRecord("home"); err != nil {
		t.Fatalf("stored bytes should still verify: %v", err)
	}
	if _, err := reader.DecodeJSONRecord("home"); err == nil || !strings.Contains(err.Error(), "does not match indexed encoded length") {
		t.Fatalf("expected encoded length mismatch, got %v", err)
	}
}

func TestUnknownWeigherVersionFallsBack(t *testing.T) {
	data := bytes.Clone(buildPack(t, ContentPlans, "build-1", map[string][]byte{"home": planJSON("home")}))
	binary.BigEndian.PutUint32(data[offWeigherVersion:], WeigherVersion+7)
	reader := openPack(t, data, ContentPlans)
	if reader.WeigherVersion() != WeigherVersion+7 {
		t.Fatalf("unexpected weigher version %d", reader.WeigherVersion())
	}
	rec, _ := reader.Record("home")
	wantDecoded, wantPeak := FallbackWeights(ContentPlans, rec.EncodedLen)
	if rec.DecodedWeight != wantDecoded || rec.PeakWeight != wantPeak {
		t.Fatalf("weights (%d, %d), want fallback (%d, %d)", rec.DecodedWeight, rec.PeakWeight, wantDecoded, wantPeak)
	}
}

func TestBuilderValidation(t *testing.T) {
	if _, err := NewBuilder(ContentPlans, ""); err == nil {
		t.Fatal("expected empty build ID to be rejected")
	}
	if _, err := NewBuilder(ContentPlans, strings.Repeat("x", MaxBuildIDLen+1)); err == nil {
		t.Fatal("expected oversized build ID to be rejected")
	}
	if _, err := NewBuilder(ContentType(0), "build-1"); err == nil {
		t.Fatal("expected invalid content type to be rejected")
	}

	plans, err := NewBuilder(ContentPlans, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := plans.AddPlan("home", []byte(`{"broken`)); err == nil {
		t.Fatal("expected invalid plan JSON to be rejected")
	}
	if err := plans.AddPlan("home", planJSON("other")); err == nil || !strings.Contains(err.Error(), "does not match plan routeId") {
		t.Fatalf("expected route ID mismatch, got %v", err)
	}
	if err := plans.AddStatic("home?", []byte(`{}`)); err == nil {
		t.Fatal("expected AddStatic on a plans pack to be rejected")
	}
	if err := plans.AddPlan("home", planJSON("home")); err != nil {
		t.Fatal(err)
	}
	if err := plans.AddPlan("home", planJSON("home")); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected duplicate key, got %v", err)
	}

	static, err := NewBuilder(ContentStatic, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := static.AddPlan("home", planJSON("home")); err == nil {
		t.Fatal("expected AddPlan on a static pack to be rejected")
	}
	if err := static.AddStatic("home?", []byte(`{"a":`)); err == nil {
		t.Fatal("expected invalid static JSON to be rejected")
	}
	if err := static.AddStatic("home?", []byte(`{}{}`)); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("expected trailing JSON to be rejected, got %v", err)
	}
	if err := static.AddStatic("", []byte(`{}`)); err == nil {
		t.Fatal("expected empty key to be rejected")
	}
	if err := static.AddStatic(strings.Repeat("k", MaxKeyLen+1), []byte(`{}`)); err == nil {
		t.Fatal("expected oversized key to be rejected")
	}
	if static.Len() != 0 {
		t.Fatalf("rejected entries must not be recorded, got %d", static.Len())
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing"+ExtPlans), ContentPlans); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
