package cache

// ActionAPIVersion identifies the wire shape of ActionEnvelope. Bump it when
// the envelope's shape changes in a way a client must branch on; purely
// additive optional fields do not require a bump.
const ActionAPIVersion = "gobeyond.action/v1alpha1"

// ActionEnvelope is the frozen action-response wire shape (Locked decision
// 9). runtime.serveAction emits this shape on every successful action, and
// the packages/react client (see fetchActionEnvelope in
// packages/react/src/actions.ts) parses it and refreshes any paths the
// action recorded, shipped together per Locked decision 9.
//
// JSON shape:
//
//	{
//	  "apiVersion": "gobeyond.action/v1alpha1",
//	  "buildId": "<build id>",
//	  "data": <opaque per-action result>,
//	  "refresh": {
//	    "paths": ["/products/widget"],
//	    "tags": ["products", "product:widget"]
//	  }
//	}
//
// "refresh" is omitted entirely when the action recorded no RevalidatePath /
// RevalidateTag calls on its RequestScope.
type ActionEnvelope struct {
	APIVersion string         `json:"apiVersion"`
	BuildID    string         `json:"buildId"`
	Data       any            `json:"data,omitempty"`
	Refresh    *ActionRefresh `json:"refresh,omitempty"`
}

// ActionRefresh lists the paths and tags an action wants the client to
// refresh, e.g. by re-fetching the current route's runtime JSON or dropping
// its router cache entry. Field names are part of the frozen wire contract;
// see ActionEnvelope's JSON shape.
type ActionRefresh struct {
	Paths []string `json:"paths,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

// ActionRefreshFromScope builds the ActionEnvelope "refresh" field from the
// paths/tags recorded on scope during one action request. It returns nil
// when nothing was recorded, so the field is omitted from the JSON body
// entirely rather than serialized as an empty object.
func ActionRefreshFromScope(scope *RequestScope) *ActionRefresh {
	if scope == nil {
		return nil
	}
	paths := scope.RefreshPaths()
	tags := scope.RefreshTags()
	if len(paths) == 0 && len(tags) == 0 {
		return nil
	}
	return &ActionRefresh{Paths: paths, Tags: tags}
}
