// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package upstream is retained as an empty placeholder. The previous HTTP
// committee query client was retired when newsletter-service moved to the
// `lfx.committee-api.list_members` NATS subject for member resolution (see
// internal/infrastructure/nats/committee_client.go). That HTTP path used to
// forward the inbound bearer token, but Heimdall mints a JWT the query-service
// can't validate as OIDC — so FGA filtered the response empty in production.
// The NATS path mirrors how lfx-v2-committee-service resolves project metadata
// (no token on the wire; trust is enforced upstream of NATS).
package upstream
