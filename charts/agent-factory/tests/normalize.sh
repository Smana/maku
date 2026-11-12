#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# normalize.sh < render > normalized: masks what Helm generates afresh on every render (the CA and
# broker certificate, the database passwords and the DSNs built from them), so two renders of the
# same values compare equal with diff. Used by render.sh's demo golden.
set -euo pipefail
sed -E \
  -e 's/^( *(tls\.crt|tls\.key|ca\.crt): ).+$/\1<generated>/' \
  -e 's/^( *(postgres|rooms_owner|rooms_broker|rooms_retention): )"[A-Za-z0-9]{32}"$/\1"<generated>"/' \
  -e 's#(postgresql://[a-z_]+:)[A-Za-z0-9]{32}@#\1<generated>@#'
