#!/bin/sh
# Сборка ZapretAuto.exe (нужен Go 1.22+). Версия: VERSION=v1.0.1 ./build.sh
VERSION="${VERSION:-dev}"
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.appVersion=$VERSION" -o ZapretAuto.exe .
