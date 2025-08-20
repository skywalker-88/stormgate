# StormGate Test Scripts

These bash scripts provide manual smoke and stress tests for StormGate.

## Files
- `rl_smoke.sh` – sequential, comprehensive checks
- `rl_blast.sh` – simple concurrency probe
- `README.md` – this doc

## Usage

```bash
# Run sequential smoke tests (basic correctness + headers)
bash demo-scripts/rl_smoke.sh

# Run parallel blast (tallies 200 vs 429 under load)
bash demo-scripts/rl_blast.sh
