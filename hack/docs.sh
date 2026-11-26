#!/usr/bin/env bash
# Reader pages state status once (README) and never per row: this refuses the status vocabulary
# that let the docs drift (review 2026-10-15), and dead relative links and anchors. Specs and
# plans under docs/superpowers speak of phases by design and are skipped, also when named
# as an argument.
#
# Links: only `](target)` outside fenced code blocks and inline code are checked; absolute
# URLs (scheme:, //) are skipped. A relative target must exist; a #fragment on a markdown
# target must match a heading's GitHub slug: lowercase, backticks/emphasis dropped, link
# markup reduced to its text, every character but letters, digits, `_`, `-` and space
# removed, spaces to `-`, and a repeated slug gets -1, -2 ... in document order.
set -euo pipefail
export LC_ALL=C.UTF-8
cd "$(dirname "$0")/.."
go run ./hack/gen-reasons.go
go run ./hack/gen-policy-explainer.go
go run ./cmd/maku config validate --config examples/merge-policies/factory.yaml examples/merge-policies/docs-scoped.yaml
if [ $# -eq 0 ]; then
  files=()
  while IFS= read -r f; do files+=("$f"); done < <(find README.md CONTRIBUTING.md AGENTS.md docs charts images -name '*.md' -not -path 'docs/superpowers/*' 2>/dev/null | sort)
else
  files=("$@")
fi

# status FILE: "line<TAB>word" for each status word, whole words only, outside code.
# A hit needs a non-word character (not a letter, digit or hyphen) on both sides, so
# Unplanned, in reviewer, not built-in and MAP-1 stay clean.
status() {
  awk '
    /^ ? ? ?(```|~~~)/ { fence = !fence; next }
    fence { next }
    {
      s = $0
      gsub(/`[^`]*`/, " ", s)
      re = "(^|[^[:alnum:]-])(AP-[0-9]+|[(]phase [0-9]+|phase [0-9]+ /|Planned|planned,|not built|not deployed|Nothing is deployed|Not merged|in review)([^[:alnum:]-]|$)"
      while (match(s, re)) {
        m = substr(s, RSTART, RLENGTH)
        sub(/^[^[:alnum:](]/, "", m); sub(/[^[:alnum:]]$/, "", m)
        if (m ~ /^planned$/) m = "planned,"
        print NR "\t" m
        s = substr(s, RSTART + RLENGTH - 1)
      }
    }' "$1"
}

# slugs FILE: one GitHub heading slug per line, fenced code skipped, repeats numbered.
# perl, not awk: lowercasing and "letter" must hold for any script (Démarrage, Écrire),
# and the default awk on Ubuntu (mawk) is byte-only. Only punctuation and symbols go.
slugs() {
  perl -CSDA -ne '
    if (/^ {0,3}(```|~~~)/) { $f = !$f; next }
    next if $f || !/^#{1,6}[ \t]/;
    s/^#+[ \t]+//; s/[ \t]+#+[ \t]*$//; s/\n$//;
    s/\]\([^)]*\)//g; s/[\[\]]//g;
    $_ = lc;
    s/[^\p{L}\p{N}\p{M}_ \t-]//g;
    s/[ \t]/-/g;
    $n = $seen{$_}++;
    print $n ? "$_-$n\n" : "$_\n";
  ' "$1"
}

# links FILE: "line<TAB>target" for each relative-looking markdown link outside code.
links() {
  awk '
    /^[ ]? ? ?(```|~~~)/ { fence = !fence; next }
    fence { next }
    {
      s = $0
      gsub(/`[^`]*`/, "", s)
      while (match(s, /\]\([^) ]+/)) {
        t = substr(s, RSTART + 2, RLENGTH - 2)
        s = substr(s, RSTART + RLENGTH)
        sub(/^</, "", t); sub(/>$/, "", t)
        if (t ~ /^[A-Za-z][A-Za-z0-9+.-]*:/ || t ~ /^\/\//) continue
        print NR "\t" t
      }
    }' "$1"
}

# ids FILE: "line<TAB>rule<TAB>match" for each design id or cluster name, outside code. A design
# id (D11, AM6, OD-8, "rollout step 7", "release N+1") names a ruling in a spec or plan, and a
# cluster name (aws-0) names one host: neither means anything to a reader, so both stay under
# docs/superpowers and docs/contributing. Whole words (no letter, digit or hyphen on either side),
# case-sensitive: B1, MD5, D100 and `D4` stay clean.
ids() {
  perl -CSDA -ne '
    if (/^ {0,3}(```|~~~)/) { $f = !$f; next }
    next if $f;
    s/`[^`]*`/ /g;
    my ($l, $r) = (q{(?<![\p{L}\p{N}-])}, q{(?![\p{L}\p{N}-])});
    while (/$l(D\d{1,2}|AM\d+|MP\d+|HR\d+|FA\d+|SI\d+|GW\d+|OD-\d+|V1\d|C[24]|R\d{1,2}|rollout step \d|step \d (?:PR [A-Z]\d*|R\d+)|[Rr]elease N(?:\+1)?|Live proof \d)$r/g) {
      print "$.\tdesign id\t$1\n";
    }
    while (/$l((?:aws|gcp)-0)$r/g) { print "$.\tcluster name\t$1\n" }
  ' "$1"
}

# ceiling FILE: checks the word ceiling declared by <!-- page-type: T --> within the first 5 lines.
# Untyped files are ignored until every reader page carries a marker.
ceiling() {
  local f=$1 t w limit
  t=$(awk 'NR<=5 && match($0, /<!--[[:space:]]*page-type:[[:space:]]*[^[:space:]]+[[:space:]]*-->/) {
    s = substr($0, RSTART, RLENGTH)
    sub(/^<!--[[:space:]]*page-type:[[:space:]]*/, "", s)
    sub(/[[:space:]]*-->$/, "", s)
    print s; exit
  }' "$f")
  [ -z "$t" ] && return 0
  case "$t" in
    tutorial|how-to) limit=1500 ;;
    explanation|operations) limit=4000 ;;
    reference) limit=0 ;;
    moved) limit=40 ;;
    *) echo "$f: unknown page-type: $t"; return 1 ;;
  esac
  [ "$limit" -eq 0 ] && return 0
  w=$(wc -w < "$f" | tr -d '[:space:]')
  if [ "$w" -gt "$limit" ]; then
    if [ "$t" = moved ]; then
      echo "$f: moved stub over 40 words"
    else
      echo "$f: over ceiling: $w words, $t allows $limit"
    fi
    return 1
  fi
}

rc=0
for f in "${files[@]}"; do
  case "$f" in *docs/superpowers/*) continue;; esac
  if ! ceiling "$f"; then rc=1; fi
  while IFS=$'\t' read -r n word; do
    echo "$f:$n: status vocabulary: $word"; rc=1
  done < <(status "$f" | awk '!seen[$0]++')
  case "$f" in
    *docs/contributing/*) ;;
    *) while IFS=$'\t' read -r n rule m; do
         echo "$f:$n: $rule: $m"; rc=1
       done < <(ids "$f" | awk '!seen[$0]++');;
  esac
  dir=$(dirname "$f")
  while IFS=$'\t' read -r n link; do
    target=${link%%#*}; frag=""
    case "$link" in *#*) frag=${link#*#};; esac
    path="$f"; [ -n "$target" ] && path="$dir/$target"
    if [ ! -e "$path" ]; then echo "$f:$n: dead link: $link"; rc=1; continue; fi
    if [ -n "$frag" ] && [ -f "$path" ]; then
      case "$path" in
        *.md) if ! slugs "$path" | grep -qxF -- "$(tr '[:upper:]' '[:lower:]' <<<"$frag")"; then
                echo "$f:$n: dead anchor: $link"; rc=1
              fi;;
      esac
    fi
  done < <(links "$f")
done
exit $rc
