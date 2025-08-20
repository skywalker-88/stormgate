# StormGate Development Checklist

## 1. Routes & Scaffolding
- [x] Add `/` (banner JSON), `/read` (cheap), `/search` (expensive).
- [x] Wire basic request logging (zerolog).
- [x] **Done when**: curl to each route returns 200 with JSON.

## 2. Config & Policies
- [x] Create `configs/policies.yaml` with defaults + per-route `{rps, burst, cost}`.
- [x] Implement loader with koanf + hot reload.
- [x] **Done when**: changing YAML affects limits without rebuild.

## 3. Redis Token Bucket Limiter
- [x] Implement atomic Lua script for refill + consume.
- [x] Create Go wrapper for limiter logic.
- [x] Apply "cost" per route.
- [x] Return 429 on exceed and export Prometheus counter.
- [x] **Done when**: hammering `/search` triggers 429; Prometheus counter rises.

## 4. Edge Sanity (NGINX)
- [ ] Test `limit_req` & `limit_conn` at `:8081`.
- [ ] Enable and check access logs.
- [ ] **Done when**: bursts to `:8081` show 503 from NGINX while `:8080` uses Go limiter.

## 5. Anomaly Detection v1
- [ ] Implement EWMA + p95 calculations per route.
- [ ] Add Prometheus gauge for anomalies.
- [ ] **Done when**: synthetic spike flips gauge and reverts after cooldown.

## 6. Mitigation Ladder
- [ ] Tighten `{rps, burst}` on anomaly detection.
- [ ] Add repeat offender detection + blocklist in Redis.
- [ ] Implement cooldown to restore defaults.
- [ ] **Done when**: spike → tighter limits → cooldown visible in metrics.

## 7. Similarity Burst Check
- [ ] Track `(path+UA)` dominance and IP diversity.
- [ ] Trigger mitigation if suspicious.
- [ ] **Done when**: same-UA flood triggers suspect flag and limits.

## 8. Admin API
- [ ] Implement `GET /admin/health`, `GET /admin/policy`, `GET /admin/incidents`.
- [ ] Implement `POST /admin/block`, `POST /admin/unblock`.
- [ ] Secure with API key or basic auth.
- [ ] **Done when**: can block/unblock and list incidents via curl.

## 9. Observability Polish
- [ ] Add Prometheus metrics: requests by code/route, 429 count, anomaly gauge, active blocks.
- [ ] Build Grafana dashboard: RPS, 2xx/4xx/5xx, 429s, anomaly flags.
- [ ] **Done when**: screenshots for normal, attack, recovery.

## 10. Docs & Proof Pack
- [ ] Update README with usage instructions.
- [ ] Add `docs/` folder with Grafana screenshots, logs, architecture diagram, policy YAML.
- [ ] **Done when**: repo is review-ready with evidence.

## Misclenious

- [ ] Graceful shutdown (later): Swap ListenAndServe for a server with Shutdown(ctx) on SIGINT/SIGTERM.

