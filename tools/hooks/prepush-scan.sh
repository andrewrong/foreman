#!/usr/bin/env bash
# Pre-push privacy scan (C-09): every commit about to be pushed — its added lines,
# its message and its author/committer — must carry no local path, real address
# or token. Run by pre-commit at the pre-push stage, which sets the two refs.
set -euo pipefail
from=${PRE_COMMIT_FROM_REF:-}
to=${PRE_COMMIT_TO_REF:-HEAD}
if [ -z "$from" ] || ! git cat-file -e "$from^{commit}" 2>/dev/null; then
  # New branch on the remote: scan what no remote branch has yet.
  range=$(git rev-list "$to" --not --remotes)
else
  range=$(git rev-list "$from..$to")
fi
pat='/Users/|/Volumes/|/private/tmp|@gmail|ghp_[A-Za-z0-9]|github_pat_|sk-ant-'  # privguard:allow rule-definition
bad=0
for c in $range; do
  hits=$(git show --format= "$c" -- . ':!spec/design/privguard.yaml' ':!AGENTS.md' ':!.pre-commit-config.yaml' ':!tools/hooks/prepush-scan.sh' \
    | grep -E '^\+' | grep -E "$pat" | grep -v 'privguard:allow' || true)
  msg=$(git log -1 --format=%B "$c" | grep -E "$pat" || true)
  who=$(git log -1 --format='%ae %ce' "$c" | tr ' ' '\n' | grep -vE '@users\.noreply\.github\.com$|^noreply@anthropic\.com$' || true)
  if [ -n "$hits$msg$who" ]; then
    bad=1
    echo "commit $(git log -1 --format='%h %s' "$c"):"
    [ -n "$hits" ] && echo "$hits" | sed 's/^/  added: /' | cut -c1-160
    [ -n "$msg" ] && echo "  message: $msg" | cut -c1-160
    [ -n "$who" ] && echo "  identity not noreply: $who"
  fi
done
exit $bad
