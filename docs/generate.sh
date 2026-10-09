#!/bin/sh
#
#  Generate Swagger API Docs
#
#  Usage:
#   go install github.com/swaggo/swag/cmd/swag@v1.16.6   # same version as in go.mod
#   cd /path/to/modbusgateway          # must be called from the project root
#   docs/generate.sh
#
swag fmt -d ./app
swag init \
  --generalInfo  main.go \
  --dir          ./cmd,./app \
  --output       ./docs \
  --parseInternal