// Package channelapi holds the OpenAPI document of /channel/v1 (main 3.0's
// ports): the contract every channel adapter — storefront, call center,
// counter terminal, vendor — is built against. main serves it at
// GET /channel/v1/openapi.json and pytest validates every /channel/v1 answer
// against it, so the document can't drift from what main does (Air doesn't
// watch .json: change this file after editing the document).
package channelapi

import _ "embed"

//go:embed openapi.json
var Spec []byte
