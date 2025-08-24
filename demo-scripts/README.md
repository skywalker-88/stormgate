# StormGate Test Scripts

These bash scripts provide manual smoke and stress tests for StormGate.

## Files
- `rl_smoke.sh` – sequential, comprehensive checks
- `rl_blast.sh` – simple concurrency probe
- `nginx_limit_smoke.sh` - validates Nginx `limit_req` by hitting Nginx directly
- `anomaly_smoke.sh` – burst that verifies anomaly metrics increase
- `README.md` – this doc

## Usage

```bash
# Run sequential smoke tests (basic correctness + headers)
bash demo-scripts/rl_smoke.sh

# Run parallel blast (tallies 200 vs 429 under load)
bash demo-scripts/rl_blast.sh

# Run sequential smoke tests and big enough burst to trip nginx (verifies the headers to check weather it reaches stromgate)
bash demo-scripts/nginx_limit_smoke.sh

# Run bursts to verify anomaly detection (anomaly counters increases)
bash demo-scripts/anomaly_smoke.sh
