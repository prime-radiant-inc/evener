# AWS Eval Infrastructure Plan

## Goal

Run 3× full terminal-bench (89 tasks × 3 reps = 267 trials) in ~30 minutes on
disposable AWS spot instances. Easy to replicate, completely isolated from
production infrastructure.

## Architecture

3 spot instances, one rep each. Each instance runs harbor with `-n 89` (all tasks
concurrent). Pre-baked AMI with all Docker images pre-pulled and caches warm.

```
┌─────────────────────────────────────────────────┐
│  eval-launcher (your laptop)                    │
│  ./eval-aws.sh launch --model X --sha Y         │
│    → launches 3 spot instances from AMI          │
│    → passes harbor command via userdata          │
│    → API keys from AWS Secrets Manager           │
│                                                  │
│  ./eval-aws.sh status                            │
│    → polls instance tags / S3 for completion     │
│                                                  │
│  ./eval-aws.sh results                           │
│    → downloads 3 result dirs from S3             │
│    → merges into single report                   │
└─────────────────────────────────────────────────┘
         │              │              │
         ▼              ▼              ▼
   ┌──────────┐  ┌──────────┐  ┌──────────┐
   │ m6i.24xl │  │ m6i.24xl │  │ m6i.24xl │
   │ 96 vCPU  │  │ 96 vCPU  │  │ 96 vCPU  │
   │ 384GB    │  │ 384GB    │  │ 384GB    │
   │ rep=1    │  │ rep=2    │  │ rep=3    │
   │          │  │          │  │          │
   │ harbor   │  │ harbor   │  │ harbor   │
   │  -n 89   │  │  -n 89   │  │  -n 89   │
   │  -k 1    │  │  -k 1    │  │  -k 1    │
   │          │  │          │  │          │
   │ apt-     │  │ apt-     │  │ apt-     │
   │ cacher-ng│  │ cacher-ng│  │ cacher-ng│
   │ (warm)   │  │ (warm)   │  │ (warm)   │
   └────┬─────┘  └────┬─────┘  └────┬─────┘
        │              │              │
        └──────────┬───┘──────────────┘
                   ▼
            ┌─────────────┐
            │  S3 bucket  │
            │  serf-evals │
            │  /run-id/   │
            │    rep-1/   │
            │    rep-2/   │
            │    rep-3/   │
            └─────────────┘
```

## Instance specs

| Resource        | Value                              |
|-----------------|------------------------------------|
| Instance type   | m6i.24xlarge (96 vCPU, 384GB RAM)  |
| Pricing         | ~$1.50/hr spot (~$2.25 total/run)  |
| Storage         | 500GB gp3 root volume              |
| Region          | us-west-1 (or us-west-2 for spot)  |
| AMI             | Custom, based on Ubuntu 24.04      |

## Timing estimate

- Image pulls: 0s (pre-baked in AMI)
- apt installs: ~30s per container (cache hits via apt-cacher-ng)
- Agent setup: ~60s (install.sh + serf binary)
- Agent execution: 1-15 min (most tasks), up to 60 min (rare 3600s tasks)
- Verifier: ~30s (uv pre-installed, pytest cached)
- **Total wall-clock: ~20-30 min for 80% of tasks, stragglers up to 60 min**

## Pre-baked AMI contents

### Software
- Docker CE
- harbor (via `uv tool install harbor`)
- apt-cacher-ng (running as systemd service)
- AWS CLI v2
- jq, tmux, htop (debugging)

### Pre-pulled Docker images
All 89 terminal-bench task images (`alexgshaw/*:20251031`), pre-pulled during
AMI build.

### Warm caches

**apt-cacher-ng cache** (`/var/cache/apt-cacher-ng/`):
During AMI build, run install-serf.sh inside each of the 89 base images through
the local apt-cacher-ng proxy. This populates the cache with every package our
install script needs (build-essential, curl, git, python3-pip, etc.) for every
base image's package manager version.

**uv binary**: Pre-installed by install-serf.sh. Since verifier test.sh runs
in the same container after the agent, `curl astral.sh/uv/... | sh` is a no-op.

### Serf agent files
- `serf-linux-amd64` binary
- `serf_agent.py` (harbor adapter)
- `install-serf.sh.j2` (install template)
- `.env` — NOT baked in. API keys come from Secrets Manager at boot.

## AWS resources needed

All in a dedicated "serf-eval" namespace, no overlap with sen-cluster.

1. **S3 bucket**: `serf-eval-results` — stores run outputs
2. **IAM role**: `serf-eval-instance` — S3 write, Secrets Manager read
3. **Security group**: `serf-eval-sg` — outbound only (HTTPS for API calls, Docker Hub)
4. **Secrets Manager secret**: `serf-eval/api-keys` — OPENAI_API_KEY, etc.
5. **Launch template**: `serf-eval-runner` — instance type, AMI, IAM role, SG
6. **Key pair**: reuse existing or create `serf-eval-key` for SSH debugging

## install-serf.sh changes

Add uv and common verifier deps to install-serf.sh.j2:

```bash
# Pre-install uv so verifier test.sh doesn't need to fetch from astral.sh
curl -LsSf https://astral.sh/uv/0.9.5/install.sh | sh || true
export PATH="$HOME/.local/bin:$PATH"

# Pre-cache common verifier dependencies
uv cache clean 2>/dev/null || true
uv pip install --system pytest==8.4.1 pytest-json-ctrf==0.3.5 2>/dev/null || true
```

## AMI build script

`tools/eval-aws/build-ami.sh`:

```bash
#!/bin/bash
# Build the serf eval AMI.
# Run on a fresh Ubuntu 24.04 instance or via Packer.

# 1. Install Docker
# 2. Install harbor: uv tool install harbor
# 3. Install apt-cacher-ng, enable as systemd service
# 4. Install AWS CLI v2
# 5. Copy serf files (binary, adapter, install template)
# 6. Pull all 89 task images:
#    harbor download -d terminal-bench@2.0
#    (or iterate task list and docker pull each)
# 7. Warm apt-cacher-ng cache:
#    for each image, run install-serf.sh through proxy
# 8. Snapshot as AMI
```

Could use Packer for reproducibility, or just a shell script run on a
throwaway instance and then `aws ec2 create-image`.

## Launch script

`tools/eval-aws/eval-aws.sh`:

```bash
#!/bin/bash
# Usage:
#   eval-aws.sh launch --model openai/gpt-5.3-codex --sha abc1234 --reps 3
#   eval-aws.sh status --run-id <id>
#   eval-aws.sh results --run-id <id>
#   eval-aws.sh terminate --run-id <id>

# launch:
#   1. Generate run-id: serf_{model}_{effort}_{sha}_{date}
#   2. For each rep (1..N):
#      - Launch spot instance from launch template
#      - Userdata script:
#        a. Fetch API keys from Secrets Manager
#        b. Start apt-cacher-ng
#        c. Run harbor:
#           harbor run -d terminal-bench@2.0 \
#             --agent-import-path serf_agent:SerfAgent \
#             -m $MODEL --ak max_rounds=100 \
#             -k 1 -n 89 \
#             --job-name ${RUN_ID}_rep${REP} \
#             --jobs-dir /data/results
#        d. Upload results to S3: aws s3 sync /data/results s3://serf-eval-results/$RUN_ID/rep-$REP/
#        e. Self-terminate: aws ec2 terminate-instances --instance-ids $(curl -s http://169.254.169.254/latest/meta-data/instance-id)
#   3. Tag instances with run-id for tracking
#
# status:
#   Check S3 for completed reps, query instance state
#
# results:
#   Download all reps from S3, merge, print summary table
#
# terminate:
#   Kill any instances tagged with run-id (safety valve)
```

## File structure

```
tools/eval-aws/
  eval-aws.sh          — main launcher script
  build-ami.sh         — AMI build script (or Packer template)
  userdata.sh.tpl      — userdata template for instances
  merge-results.py     — merges N reps into combined report
  README.md            — usage instructions
```

## Implementation order

### Task 1: install-serf.sh changes
Add uv pre-install and common verifier deps to install-serf.sh.j2.
Test on magic-kingdom with a single task run.

### Task 2: AWS resources (Terraform or CLI)
Create S3 bucket, IAM role, security group, secrets, launch template.
All in us-west-1, tagged `project=serf-eval`.

### Task 3: AMI build script
Write build-ami.sh. Run on a throwaway m6i.4xlarge to build the AMI.
Pull all 89 images, warm apt cache, install everything.
Create AMI, note the AMI ID.

### Task 4: Launch script (eval-aws.sh)
Write the launcher. Supports `launch`, `status`, `results`, `terminate`.
Userdata template with harbor command.

### Task 5: Results merger
Write merge-results.py — combines N rep directories into one report
with pass rates, per-task breakdown, comparison to baselines.

### Task 6: End-to-end test
Run a 1-rep, 3-task smoke test. Verify:
- Instance launches from AMI
- harbor runs successfully
- Results land in S3
- Instance self-terminates
- Results download works

### Task 7: Full eval run
Run the real thing: 3 reps, 89 tasks, ~30 min wall-clock.
Compare results with magic-kingdom baseline.

## Cost estimate

| Item | Cost |
|------|------|
| AMI build (one-time) | ~$2 (1hr on m6i.4xlarge spot) |
| Per eval run (3 reps) | ~$2-4 (3× m6i.24xlarge spot, 30-60 min) |
| S3 storage | ~$0.02/run (few GB) |
| Secrets Manager | $0.40/month |
| **Total per eval** | **~$3-5** |

## Open questions

1. **Instance type**: m6i.24xlarge gives 96 vCPU for 89 containers. Some tasks
   want 2+ CPUs. Do we need bigger, or is oversubscription fine?
2. **Spot interruption**: spot instances can be reclaimed. Should we use
   on-demand for reliability? Cost difference is ~3× ($4.50/hr vs $1.50/hr).
   For a 30-min run, on-demand total would be ~$7-12 — still cheap.
3. **Region**: us-west-1 (matches our existing infra) or us-west-2 (better
   spot availability, more instance types)?
4. **harbor version pinning**: should we pin harbor version in AMI to avoid
   surprises?
5. **Packer vs. script**: Packer is cleaner for AMI builds but adds a
   dependency. Plain shell script + `create-image` is simpler.
