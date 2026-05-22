// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package migrations embeds the newsletter-service SQL migration files for
// golang-migrate to apply at service startup.
package migrations

import "embed"

// FS holds the embedded migration SQL files in their lexicographic order. The
// embedded directory layout matches golang-migrate/iofs expectations.
//
//go:embed *.sql
var FS embed.FS
