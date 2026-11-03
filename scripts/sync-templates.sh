#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Refreshes the embedded newsletter template sets from their git repositories.
#
# Reads internal/service/render/declarative/templates/templates.lock. Each data
# line is "key repo-url ref". For every line this script fetches the ref,
# replaces templates/<key>/ with the repository's blocks/, bricks/, wrappers/
# and edition.json, adds the two licence header lines to every .html file, and
# rewrites the ref in the lock file to the resolved commit SHA.
#
# The repositories can be private. The fetch uses your own git credentials.
# Nothing in the build or in the running service touches git.

set -euo pipefail

cd "$(dirname "$0")/.."

TEMPLATES_DIR="internal/service/render/declarative/templates"
LOCK_FILE="${TEMPLATES_DIR}/templates.lock"
HEADER_1='<!-- Copyright The Linux Foundation and each contributor to LFX. -->'
HEADER_2='<!-- SPDX-License-Identifier: MIT -->'

if [ ! -f "${LOCK_FILE}" ]; then
	echo "missing ${LOCK_FILE}" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
new_lock="${work}/templates.lock"
: >"${new_lock}"

while IFS= read -r line || [ -n "${line}" ]; do
	case "${line}" in
	'' | '#'*)
		printf '%s\n' "${line}" >>"${new_lock}"
		continue
		;;
	esac

	read -r key url ref extra <<<"${line}"
	if [ -z "${key}" ] || [ -z "${url}" ] || [ -z "${ref}" ] || [ -n "${extra}" ]; then
		echo "bad lock line (want: key repo-url ref): ${line}" >&2
		exit 1
	fi
	case "${key}" in
	*[!a-z0-9-]*)
		echo "bad template key: ${key}" >&2
		exit 1
		;;
	esac

	echo "==> ${key}: fetching ${ref} from ${url}"
	repo="${work}/${key}"
	git init -q "${repo}"
	git -C "${repo}" fetch -q --depth 1 "${url}" "${ref}"
	sha="$(git -C "${repo}" rev-parse 'FETCH_HEAD^{commit}')"
	git -C "${repo}" -c advice.detachedHead=false checkout -q "${sha}"

	dest="${TEMPLATES_DIR}/${key}"
	rm -rf "${dest}"
	mkdir -p "${dest}"
	for sub in blocks bricks wrappers; do
		if [ -d "${repo}/${sub}" ]; then
			cp -R "${repo}/${sub}" "${dest}/${sub}"
		fi
	done
	if [ -f "${repo}/edition.json" ]; then
		cp "${repo}/edition.json" "${dest}/edition.json"
	fi

	# Sort so the result does not depend on directory order.
	find "${dest}" -type f -name '*.html' | LC_ALL=C sort | while IFS= read -r file; do
		{
			printf '%s\n%s\n' "${HEADER_1}" "${HEADER_2}"
			cat "${file}"
		} >"${file}.tmp"
		mv "${file}.tmp" "${file}"
	done

	printf '%s %s %s\n' "${key}" "${url}" "${sha}" >>"${new_lock}"
	echo "    pinned ${sha}"
done <"${LOCK_FILE}"

cp "${new_lock}" "${LOCK_FILE}"
