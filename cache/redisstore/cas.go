package redisstore

import "strconv"

// casScript is the write-time compare-and-set every queued Set runs through.
// KEYS[1] is the entry key; KEYS[2:] are the tag-version keys the record was
// built under. ARGV[1] is the PX TTL in milliseconds, ARGV[2] is the encoded
// payload, and ARGV[3:] are the expected version for each tag key at the
// matching KEYS index (ARGV[2+i] pairs with KEYS[1+i]). A tag key that does
// not exist reads as "0", matching a Record whose TagVersions never recorded
// that tag. The SET only runs when every pair matches; casAllows below is the
// pure-Go mirror of this exact decision.
const casScript = `
local n = #KEYS - 1
for i = 1, n do
	local current = redis.call('GET', KEYS[i + 1])
	if current == false then
		current = "0"
	end
	if current ~= ARGV[2 + i] then
		return 0
	end
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[1])
return 1
`

// casAllows is the reference semantics casScript implements in Lua: a write
// is allowed only when, for every tag the record was built under, the tag's
// current counter (as a decimal string, "0" when the tag key is absent)
// equals the expected version recorded on the record. Tags present in
// current but not in expected are irrelevant - the record makes no claim
// about them, so they cannot make it stale.
func casAllows(current map[string]string, expected map[string]int64) bool {
	for tag, version := range expected {
		got, ok := current[tag]
		if !ok {
			got = "0"
		}
		if got != strconv.FormatInt(version, 10) {
			return false
		}
	}
	return true
}
