#!/bin/bash

declare -a BINARIES=("conveyor-server" "conveyor-agent")
OUTPUT_DIR="dist"

set -e

echo "Starting build process..."

echo "Cleaning up old builds..."
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

for binary in "${BINARIES[@]}"; do
  echo "--- Building ${binary} ---"
  BUILD_PATH="./cmd/${binary}/"

  # Build for Linux AMD64 (x64)
  echo "Building for linux/amd64..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${binary}-linux-amd64" "$BUILD_PATH"

  # Build for Linux ARM64
  echo "Building for linux/arm64..."
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${binary}-linux-arm64" "$BUILD_PATH"

  # Build for Linux ARMv7 (armhf)
  echo "Building for linux/armhf (ARMv7)..."
  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${binary}-linux-armhf" "$BUILD_PATH"

  echo "--- Finished building ${binary} ---"
  echo ""
done

echo "Verifying all builds..."
file "$OUTPUT_DIR"/*

echo ""
echo "Build complete! All binaries are located in the '$OUTPUT_DIR' directory."