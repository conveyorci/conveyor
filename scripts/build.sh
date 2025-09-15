#!/bin/bash

BINARY_NAME="conveyor"
BUILD_PATH="./cmd/conveyor/"
OUTPUT_DIR="dist"

set -e

echo "Starting build process..."

echo "Cleaning up old builds..."
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# build for linux AMD64 (x64)
echo "Building for linux/amd64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${BINARY_NAME}-linux-amd64" "$BUILD_PATH"

# build for linux ARM64
echo "Building for linux/arm64..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${BINARY_NAME}-linux-arm64" "$BUILD_PATH"

# build for linux ARMv7 (armhf)
echo "Building for linux/armhf (ARMv7)..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o "$OUTPUT_DIR/${BINARY_NAME}-linux-armhf" "$BUILD_PATH"

echo "Verifying builds..."
file "$OUTPUT_DIR"/*

echo ""
echo "Build complete! Binaries are located in the '$OUTPUT_DIR' directory."