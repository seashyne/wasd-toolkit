# WASD Toolkit

CLI toolkit สำหรับเทส Go backend / game backend API แบบเร็ว ๆ เอาไว้ใช้ทั้ง local dev, staging และ CI.

## จุดเด่น

- health check service
- login เพื่อดึง bearer token แล้วเก็บไว้ใน `.wasd-token`
- generic API call พร้อม assertion เบื้องต้น
- scenario runner สำหรับ smoke test หลาย step
- lightweight load test สำหรับเช็ก burst / spike แบบเร็ว
- config ผ่าน JSON และรองรับ environment variable เช่น `${WASD_BASE_URL}`

## โครงสร้าง

```text
cmd/wasd-toolkit/main.go      # entrypoint CLI
internal/toolkit/toolkit.go   # config/client/scenario/load logic
examples/config.local.json    # ตัวอย่าง config
examples/scenario.basic.json  # ตัวอย่าง scenario
scripts/push-github.sh        # helper สำหรับ push ขึ้น GitHub
```

## เริ่มใช้งาน

```bash
go build -o bin/wasd-toolkit ./cmd/wasd-toolkit
```

หรือ

```bash
go run ./cmd/wasd-toolkit help
```

## ตัวอย่าง config

ไฟล์ `examples/config.local.json`

```json
{
  "base_url": "http://localhost:8080",
  "timeout_seconds": 15,
  "headers": {
    "X-Game-Client": "wasd-toolkit"
  },
  "auth": {
    "path": "/v1/auth/login",
    "method": "POST",
    "payload": {
      "email": "${WASD_EMAIL}",
      "password": "${WASD_PASSWORD}"
    },
    "token_json_path": "$.data.access_token"
  }
}
```

## คำสั่งหลัก

### 1) health check

```bash
go run ./cmd/wasd-toolkit health -config examples/config.local.json -path /health -contains ok
```

### 2) login

```bash
export WASD_EMAIL=tester@example.com
export WASD_PASSWORD=secret
go run ./cmd/wasd-toolkit login -config examples/config.local.json
```

หลัง login สำเร็จ token จะถูกเก็บไว้ที่ `examples/.wasd-token`

### 3) ยิง API เดี่ยว

```bash
go run ./cmd/wasd-toolkit call \
  -config examples/config.local.json \
  -method GET \
  -path /v1/profile \
  -expect-status 200 \
  -auth
```

### 4) รัน scenario

```bash
go run ./cmd/wasd-toolkit scenario -config examples/config.local.json -file examples/scenario.basic.json
```

ตัวอย่าง output:

```json
{
  "name": "basic backend smoke",
  "generated_at": "2026-04-13T10:00:00Z",
  "results": [
    {
      "name": "health",
      "passed": true,
      "status_code": 200,
      "duration_ms": 12,
      "body_snippet": "ok"
    }
  ]
}
```

### 5) load smoke test

```bash
go run ./cmd/wasd-toolkit load \
  -config examples/config.local.json \
  -path /health \
  -requests 200 \
  -concurrency 20
```

## ตัวอย่าง scenario

ไฟล์ `examples/scenario.basic.json`

```json
{
  "name": "basic backend smoke",
  "steps": [
    {
      "name": "health",
      "method": "GET",
      "path": "/health",
      "expect_status": 200,
      "contains": "ok"
    },
    {
      "name": "login",
      "method": "POST",
      "path": "/v1/auth/login",
      "content_type": "application/json",
      "body": "{\"email\":\"${WASD_EMAIL}\",\"password\":\"${WASD_PASSWORD}\"}",
      "expect_status": 200,
      "save_json": "$.data.access_token",
      "save_as": "token"
    },
    {
      "name": "profile",
      "method": "GET",
      "path": "/v1/profile",
      "auth": true,
      "expect_status": 200
    }
  ]
}
```

## แนะนำให้ต่อยอดเพิ่ม

- assertion แบบ JSON path เทียบค่า
- รองรับ multipart upload
- export JUnit / HTML report เพื่อใช้ใน CI
- preset สำหรับ service สำคัญ เช่น auth, player, inventory, mail, gacha
- seed/reset test data endpoint สำหรับ staging

## Push ขึ้น GitHub

```bash
bash scripts/push-github.sh
```

หรือทำเอง:

```bash
git init
git branch -M main
git remote add origin https://github.com/seashyne/wasd-toolkit.git
git add .
git commit -m "feat: bootstrap wasd toolkit"
git push -u origin main
```

> ถ้าใช้ HTTPS GitHub จะขอ login / personal access token ตอน push
