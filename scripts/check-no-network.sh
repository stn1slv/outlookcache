#!/usr/bin/env bash
# Fails when a package of this project imports a package that can open a
# network connection. The MCP SDK links net/http, so only the direct
# imports of this project's own packages are checked, for the macOS and
# the Linux build. This guards against accidental network code; it is not
# a proof (os/exec or a third-party client package would not be caught).
set -euo pipefail

deny='^(net|net/.+|crypto/tls|golang\.org/x/net/.+)$'
# Packages below net that only parse data and open no connection.
allow='^(net/url|net/mail|net/netip|net/textproto|golang\.org/x/net/html(/.+)?)$'

found=0
for goos in darwin linux; do
	pairs=$(GOOS=$goos go list -f '{{range .Imports}}{{$.ImportPath}} {{.}}{{"\n"}}{{end}}' ./cmd/... ./internal/...)
	while read -r pkg imp; do
		[[ -z "${imp:-}" ]] && continue
		if [[ $imp =~ $deny && ! $imp =~ $allow ]]; then
			echo "$pkg imports $imp ($goos build)"
			found=1
		fi
	done <<<"$pairs"
done
if ((found)); then
	echo "A package of this project imports a network package."
	exit 1
fi
echo "No package of this project imports a network package."
