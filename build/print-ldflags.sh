#!/bin/bash

version=$($(dirname "${BASH_SOURCE}")/print-version.sh)

echo "-ldflags \"-X sigs.k8s.io/kubectl-check-ownerreferences/pkg.Version=${version}\""
