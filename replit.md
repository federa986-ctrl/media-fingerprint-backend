# Media Fingerprint Backend

An ephemeral Go media fingerprinting API that returns whole-file hashes and per-second image, audio, and video fingerprints without retaining uploads.

## Run & Operate

- `pnpm --filter @workspace/api-server run dev` — run the fingerprint API (port 8080)
- `pnpm run typecheck` — full typecheck across all packages
- `pnpm --filter @workspace/api-server run build` — build the Go fingerprint server
- `pnpm --filter @workspace/api-spec run codegen` — regenerate API hooks and Zod schemas from the OpenAPI spec
- `cd artifacts/api-server && go test ./...` — run fingerprint accuracy and worker tests

## Stack

- pnpm workspaces, Node.js 24, TypeScript 5.9
- API: Go `net/http`
- Media decode: FFmpeg / FFprobe
- Fingerprinting: native Go worker pool, DCT perceptual hashes, normalized audio signatures
- API codegen: Orval (from OpenAPI spec)
- Build: Go binary

## Where things live

- `artifacts/api-server/cmd/fingerprint-server` — HTTP entrypoint
- `artifacts/api-server/internal/fingerprint` — upload streaming, queue, parallel stream extraction, hashing, and tests
- `lib/api-spec/openapi.yaml` — source-of-truth API contract

## Architecture decisions

- Uploads use short-lived OS temp files only because FFmpeg needs a seekable input; each file is removed after the request completes.
- A bounded worker queue rejects excess work with HTTP 429 instead of allowing unbounded memory or process growth.
- Admission control happens before upload bytes are written, and a global temporary-byte budget caps disk usage during bursts.
- Video and audio extraction run concurrently inside each job; stream sampling defaults to one window per second.
- Whole-file SHA-256 and MD5 are returned alongside perceptual hashes so callers can choose exact or similarity matching.
- Exact byte-identical content can be matched deterministically with SHA-256; perceptual matching is similarity-based and cannot honestly guarantee 100% across arbitrary re-encoding or edits.

## Product

The service accepts multipart uploads (`media` or `file`) and raw binary bodies, returning exact content hashes, image perceptual hashes, per-second video frame hashes, and per-second normalized audio signatures.

## User preferences

The service must not persist uploaded media.

## Gotchas

- FFmpeg and FFprobe must be available on the runtime PATH.
- `FINGERPRINT_MAX_UPLOAD_BYTES`, `FINGERPRINT_MAX_TEMP_BYTES`, `FINGERPRINT_WORKERS`, `FINGERPRINT_QUEUE_SIZE`, `FINGERPRINT_MAX_CONNECTIONS`, `FINGERPRINT_HTTP_TIMEOUT_SECONDS`, `FINGERPRINT_INTERVAL_MS`, and `FINGERPRINT_MAX_SAMPLES` tune safety and throughput.

## Pointers

- See the `pnpm-workspace` skill for workspace structure, TypeScript setup, and package details
