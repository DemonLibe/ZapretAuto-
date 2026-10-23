#!/bin/sh
# Сборка ZapretAuto.exe (из Linux/macOS/Windows с установленным Go 1.22+)
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o ZapretAuto.exe .
