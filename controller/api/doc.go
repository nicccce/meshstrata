// Package api embeds the v1 public contract for tooling. It registers no routes.
// x-implemented=false operations must stay unregistered until their security owner
// and business owner deliver them. JSON schemas are design contracts, not runtime auth.
package api

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte

//go:embed control.schema.json
var ControlSchema []byte

//go:embed control-v2.schema.json
var ControlV2Schema []byte

//go:embed bundle.schema.json
var BundleSchema []byte

//go:embed bundle-v2.schema.json
var OfflineBundleV2Schema []byte
