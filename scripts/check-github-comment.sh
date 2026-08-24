#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [comment-file]" >&2
  exit 2
fi

if [ "$#" -eq 1 ]; then
  comment=$(<"$1")
else
  comment=$(cat)
fi

if printf '%s' "$comment" | perl -CSD -ne 'exit 1 if /[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]/'; then
  exit 0
fi

echo "GitHub comments must be written in English; Chinese characters were found." >&2
exit 1
