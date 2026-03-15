#!/bin/sh
#
#  Generate Swagger API Docs
#
#  Usage:
#   go install github.com/swaggo/swag/cmd/swag@latest
#   cd /path/to/modbusgateway          # must be called from the project root
#   docs/generate.sh
#
swag fmt -d ./app
swag init \
  --generalInfo  main.go \
  --dir          ./cmd,./app \
  --output       ./docs \
  --parseInternal