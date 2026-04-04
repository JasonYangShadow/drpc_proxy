#!/usr/bin/env bash
set -e

echo "Installing Go tools..."
go install github.com/onsi/ginkgo/v2/ginkgo@latest
go install github.com/onsi/gomega/...@latest

echo "✓ Ginkgo and Gomega installed successfully"