// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashUserPrincipal returns the lowercase hex SHA-256 of the user principal, the
// shape the newsletter_shares and newsletter_page_views user_hash CHECK
// constraints enforce. Recording and erasure on both tables must use it, so
// changing the hash breaks both and strands existing rows from erasure.
func hashUserPrincipal(user string) string {
	sum := sha256.Sum256([]byte(user))
	return hex.EncodeToString(sum[:])
}
