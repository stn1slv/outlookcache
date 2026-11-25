#!/usr/bin/env bash
# Writes the license texts of the Go standard library and of every module
# linked into the outlookcache binary below the marker line of
# THIRD_PARTY_NOTICES.md. Run it after a dependency change (make notices).
set -euo pipefail

file=THIRD_PARTY_NOTICES.md
marker='<!-- The part below is written by scripts/third-party-notices.sh. -->'

license_of() {
	local dir=$1 f
	for f in LICENSE LICENSE.txt LICENSE.md LICENCE COPYING; do
		if [[ -f $dir/$f ]]; then
			echo "$dir/$f"
			return
		fi
	done
	echo "no license file in $dir" >&2
	exit 1
}

goroot=$(go env GOROOT)
golicense=$goroot/LICENSE
[[ -f $golicense ]] || golicense=$goroot/../LICENSE

modules=$(GOOS=darwin go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' ./cmd/outlookcache | sort -u)

head=$(awk -v m="$marker" '$0 == m { exit } { print }' "$file")
{
	printf '%s\n\n%s\n\n' "$head" "$marker"
	echo '## Go modules in the binary'
	echo
	echo 'The `outlookcache` binary contains the Go standard library and the following modules. Their licenses follow.'
	printf '\n### Go standard library\n\n```text\n'
	cat "$golicense"
	echo '```'
	while read -r path dir; do
		printf '\n### %s\n\n```text\n' "$path"
		cat "$(license_of "$dir")"
		echo '```'
	done <<<"$modules"
} >"$file.tmp"
mv "$file.tmp" "$file"
