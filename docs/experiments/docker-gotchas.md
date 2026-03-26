# Docker Gotchas on flower-garden

## Network address pool exhaustion
- Default Docker config has ~31 /24 networks available
- Running 89 tasks with `-n 4` exhausts this if networks aren't cleaned up fast
- Fix: expanded pool in `/etc/docker/daemon.json`:
  ```json
  {"default-address-pools": [{"base": "172.17.0.0/12", "size": 24}]}
  ```
- This gives 4096 networks instead of ~31

## NEVER use `docker system prune -af` on flower-garden
- This removes ALL cached images
- Docker Hub rate limits anonymous pulls to 100/6hrs
- Rebuilding image cache takes hours
- Use `docker system prune -f` (without `-a`) to only remove dangling images
- Use `docker network prune -f` to clean networks without touching images

## Pre-run cleanup checklist
1. `docker network prune -f` — remove stale networks
2. `docker container prune -f` — remove stopped containers
3. `docker volume prune -f` — remove unused volumes
4. Do NOT prune images (`-a` flag)

## API key must be sourced in nohup scripts
- Harbor runs from serf_agent.py which reads `os.environ[OPENAI_API_KEY]`
- The .env file at `~/git/terminal-bench/.env` has the key but isn't auto-loaded
- nohup scripts MUST include: `cd ~/git/terminal-bench && export $(cat .env | xargs)`
- Also need: `export PATH="$HOME/.local/bin:$PATH"` for harbor binary
- Without the API key, serf fails with "no LLM providers configured"
