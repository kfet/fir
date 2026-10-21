#!/usr/bin/env bash
# List every tracked shell script: *.sh files plus files with a sh/bash shebang.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
{
	git ls-files -z '*.sh' | tr '\0' '\n'
	git ls-files -z | while IFS= read -r -d '' f; do
		[[ -f $f && $f != *.sh ]] || continue
		IFS= read -r first <"$f" 2>/dev/null || continue
		[[ $first =~ ^\#!.*[/[:space:]](ba)?sh([[:space:]]|$) ]] && printf '%s\n' "$f"
	done
} | sort -u
