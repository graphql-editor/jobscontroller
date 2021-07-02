#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

PKG=$(go list)
SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=$(go list -f '{{.Dir}}' -m k8s.io/code-generator)

WORKDIR=$(mktemp -d)
rm -rf ${SCRIPT_ROOT}/pkg/generated
find ${SCRIPT_ROOT}/pkg/apis -name 'zz_*.go' -exec rm {} \;
bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy,client,informer,lister" \
  $PKG/pkg/generated $PKG/pkg/apis \
  jobscontroller:v1alpha1 \
  --output-base "${WORKDIR}" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate.go.txt

find $WORKDIR -name '*.go' -exec \
	sh -c 'fn=$(echo "$1" | sed "s%${3}%${2}%g"); mkdir -p $(dirname $fn); mv $1 $fn' -- \
	{} "$SCRIPT_ROOT" "${WORKDIR}/${PKG}" \;
rm -r ${WORKDIR}
