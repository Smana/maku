#!/bin/sh
# The harness userland contract: what every harness image (OpenHands, OpenCode) carries for the agent
# and for agent-checks, whose steps are `bash -c` lines that call a repository's own scripts. Both
# images' test stages run it, so a base-image change that drops a tool fails the build, never a run.
# POSIX sh on purpose: it must report a missing bash rather than fail to start.
set -eu

fail=0
no() { echo "userland contract: $*" >&2; fail=1; }

# bash, the GNU userland repositories' scripts assume, the archive formats mise and scripts unpack,
# an HTTP client for scripts behind the run's egress proxy, jq, git, rsync, ripgrep (search) and python3.
for b in bash env ls cp mv rm mkdir mktemp cat head tail cut tr sort uniq wc tee xargs realpath readlink \
  sha256sum base64 timeout date find grep sed awk diff cmp make tar gzip xz curl jq git rsync rg python3; do
  command -v "$b" >/dev/null 2>&1 || no "$b is missing"
done

# The GNU flavours, not busybox's: flags the scripts use that busybox lacks or reads differently.
for b in grep sed find sort diff xargs tar; do
  "$b" --version 2>/dev/null | head -1 | grep -q GNU || no "$b is not GNU's"
done
printf 'a1\n' | grep -qP '\d' 2>/dev/null || no "grep -P does not work"
[ "$(printf '1.10\n1.9\n' | sort -V | head -1)" = 1.9 ] || no "sort -V does not work"

# bash runs a step the way agent-checks runs it, bash features included.
out=$(bash -c 'set -o pipefail; a=(x y); [[ ${#a[@]} == 2 ]] && echo "${a[1]}"' 2>&1) || true
[ "$out" = y ] || no "bash -c did not run a bash script (got: $out)"

# The system python3 the checks run imports yaml.
python3 -c 'import yaml' 2>/dev/null || no "the system python3 cannot import yaml"

# glibc-linked tools mise installs (hugo extended links glibc and libstdc++): the glibc loader for
# this architecture, native or gcompat's, and libstdc++.
case "$(uname -m)" in
  x86_64) ld=/lib64/ld-linux-x86-64.so.2 ;;
  aarch64) ld=/lib/ld-linux-aarch64.so.1 ;;
  *) ld= ; no "unknown architecture $(uname -m)" ;;
esac
[ -z "$ld" ] || [ -e "$ld" ] || no "no glibc loader at $ld"
[ -n "$(find /lib /usr/lib -name 'libstdc++.so.6' 2>/dev/null | head -1)" ] || no "no libstdc++.so.6"

[ "$fail" = 0 ] && echo "userland contract: met"
exit "$fail"
