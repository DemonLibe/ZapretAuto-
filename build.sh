#!/bin/sh
# Сборка ZapretAuto.exe (нужен Go 1.22+). Версия: VERSION=v1.0.1 ./build.sh
# Иконка, манифест и сведения о версии берутся из winres/ (go install github.com/tc-hib/go-winres@v0.3.3).
set -e
VERSION="${VERSION:-dev}"
NUM=$(echo "$VERSION" | sed -n 's/^v\{0,1\}\([0-9][0-9.]*\).*/\1/p')
WINRES=$(command -v go-winres || echo "$(go env GOPATH)/bin/go-winres")
if [ -x "$WINRES" ]; then
  "$WINRES" make --in winres/winres.json --arch amd64 --product-version "${NUM:-0.0.0}" --file-version "${NUM:-0.0.0}"
fi
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.appVersion=$VERSION" -o ZapretAuto.exe .
