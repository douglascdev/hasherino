#!/usr/bin/env bash
ai-jail --network --terminal-passthrough --x11 \
  --map "$XDG_RUNTIME_DIR" \
  --rw-map /tmp \
  --env NIX_LDFLAGS \
  --env NIX_CFLAGS_COMPILE \
  --env NIX_BINTOOLS_WRAPPER_TARGET_HOST_x86_64_unknown_linux_gnu \
  --env NIX_CC_WRAPPER_TARGET_HOST_x86_64_unknown_linux_gnu \
  --env PKG_CONFIG_PATH \
  --env CGO_ENABLED \
  opencode
