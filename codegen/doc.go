// Package codegen decodes GoBeyond value-contract documents and generates the
// Go types shared by page loaders and actions.
//
// Generated object fields are emitted in lexical JSON-name order. Optional and
// nullable values use pointers. A schema marked both optional and nullable has
// the same Go representation as either flag alone, so absence and JSON null are
// intentionally collapsed in the MVP wire model.
//
// The MVP supports unions only when every variant is a string literal or a
// string enum. Such unions become named string types with constants. Structural
// and mixed unions are rejected because encoding/json cannot decode them into a
// safe, unambiguous generated representation without an explicit discriminator
// contract.
package codegen
