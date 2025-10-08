package schema

import _ "embed"

// StackV1Schema contains the JSON schema for stack manifests.
//
//go:embed stack.v1.json
var StackV1Schema []byte
