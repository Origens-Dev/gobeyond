package redisstore

// tagKey builds the Redis key holding one tag's version counter. namespace
// is Options.Namespace (the deploy prefix), kept separate from entry keys'
// own namespacing so a tag counter never collides with an entry key that
// happens to share a segment.
func tagKey(namespace, tag string) string {
	if namespace == "" {
		return "tag:" + tag
	}
	return namespace + "/tag/" + tag
}

// tagBumpChannel builds the pub/sub channel BumpTag publishes to and
// SubscribeTagBumps listens on for one namespace.
func tagBumpChannel(namespace string) string {
	if namespace == "" {
		return "gobeyond/tagbump"
	}
	return namespace + "/tagbump"
}
