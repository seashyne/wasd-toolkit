#!/usr/bin/env bash
set -euo pipefail

git init
git branch -M main
git remote remove origin 2>/dev/null || true
git remote add origin https://github.com/seashyne/wasd-toolkit.git
git add .
git commit -m "feat: bootstrap wasd toolkit"
git push -u origin main
