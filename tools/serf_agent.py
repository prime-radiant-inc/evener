"""Harbor adapter for serf — a non-interactive coding agent."""

import logging
import os
import platform
import shlex
import shutil
from pathlib import Path

from harbor.agents.installed.base import BaseInstalledAgent, ExecInput
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


_AGENT_DIR = Path(__file__).parent

PROVIDER_ENV_KEYS = {
    "openai": "OPENAI_API_KEY",
    "anthropic": "ANTHROPIC_API_KEY",
    "google": "GEMINI_API_KEY",
}

# Harbor bind-mounts /logs/agent/ for us; writing state there ensures it
# survives container teardown and ends up in the job output automatically.
_CONTAINER_STATE_DIR = "/logs/agent/serf-state"

_ARTIFACT_EXCLUDES = [
    ".git", "node_modules", "__pycache__", ".venv",
    "*.pyc", "*.o", "*.so", ".cache",
]
_ARTIFACT_WARN_MB = 100

logger = logging.getLogger(__name__)


class SerfAgent(BaseInstalledAgent):
    """Serf agent: headless, non-interactive coding agent."""

    def __init__(self, max_rounds: int = 100, min_result_round: int = 0, reasoning_effort: str = "", enable_reviewer_gate: bool = False, result_tool_name: str = "", *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._max_rounds = max_rounds
        self._min_result_round = min_result_round
        self._reasoning_effort = reasoning_effort
        self._enable_reviewer_gate = enable_reviewer_gate
        self._result_tool_name = result_tool_name

        # Parse provider/model from model_name (e.g. "openai/gpt-5.2-codex")
        if self._parsed_model_provider:
            self._provider = self._parsed_model_provider
            self._model = self._parsed_model_name
        else:
            self._provider = os.environ.get("SERF_PROVIDER", "openai")
            self._model = self._parsed_model_name or "gpt-5.2-codex"

    @staticmethod
    def name() -> str:
        return "serf"

    @property
    def _install_agent_template_path(self) -> Path:
        return _AGENT_DIR / "install-serf.sh.j2"

    async def setup(self, environment: BaseEnvironment) -> None:
        # Create /installed-agent and upload serf binaries before the install script runs
        await environment.exec(command="mkdir -p /installed-agent")
        for arch in ("amd64", "arm64"):
            binary = _AGENT_DIR / f"serf-linux-{arch}"
            if binary.exists():
                await environment.upload_file(
                    source_path=binary,
                    target_path=f"/installed-agent/serf-linux-{arch}",
                )

        # Renders template, uploads install.sh, executes it
        await super().setup(environment)

    def create_run_agent_commands(self, instruction: str) -> list[ExecInput]:
        escaped = shlex.quote(instruction)

        env_key = PROVIDER_ENV_KEYS.get(self._provider)
        env: dict[str, str] = {}
        if env_key and env_key in os.environ:
            env[env_key] = os.environ[env_key]

        effort_flag = (
            f"--reasoning-effort {self._reasoning_effort} "
            if self._reasoning_effort
            else ""
        )

        min_result_flag = (
            f"--min-result-round {self._min_result_round} "
            if self._min_result_round > 0
            else ""
        )

        reviewer_gate_flag = (
            "--enable-reviewer-gate "
            if self._enable_reviewer_gate
            else ""
        )

        result_tool_name_flag = (
            f"--result-tool-name {self._result_tool_name} "
            if self._result_tool_name
            else ""
        )

        export_atif_flag = f"--export-atif {_CONTAINER_STATE_DIR}/trajectory.json "

        return [
            ExecInput(
                command=(
                    f"serf --provider {self._provider} "
                    f"--model {self._model} "
                    f"--max-rounds {self._max_rounds} "
                    f"{min_result_flag}"
                    f"{reviewer_gate_flag}"
                    f"{result_tool_name_flag}"
                    f"--state-dir {_CONTAINER_STATE_DIR} "
                    f"{export_atif_flag}"
                    f"{effort_flag}"
                    f"-- {escaped}"
                ),
                env=env,
            ),
        ]

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        try:
            await super().run(instruction, environment, context)
        finally:
            # Download serf session traces from the container, even on timeout.
            local_state_dir = self.logs_dir / "serf-state"
            try:
                await environment.download_dir(_CONTAINER_STATE_DIR, local_state_dir)
                logger.info("Downloaded serf traces to %s", local_state_dir)
            except Exception as e:
                logger.warning("Could not download serf traces: %s", e)

            # Copy ATIF trajectory to logs_dir root for harbor viewer.
            traj_src = local_state_dir / "trajectory.json"
            if traj_src.exists():
                shutil.copy2(traj_src, self.logs_dir / "trajectory.json")
                logger.info("Copied ATIF trajectory to %s", self.logs_dir / "trajectory.json")

            # Extract agent artifacts from /app (filtered).
            # Harbor's download_dir doesn't support exclude, so we download
            # everything and prune locally.
            artifacts_dir = self.logs_dir / "artifacts"
            try:
                await environment.download_dir("/app", artifacts_dir)
                _prune_artifacts(artifacts_dir, _ARTIFACT_EXCLUDES)
                total = _dir_size(artifacts_dir)
                if total > _ARTIFACT_WARN_MB * 1024 * 1024:
                    logger.warning(
                        "Large artifacts: %dMB in /app (threshold: %dMB)",
                        total // (1024 * 1024), _ARTIFACT_WARN_MB,
                    )
            except Exception as e:
                logger.warning("Could not download /app artifacts: %s", e)

    def populate_context_post_run(self, context: AgentContext) -> None:
        # ATIF trajectory is produced by the Go binary (--export-atif flag)
        # and copied to logs_dir/trajectory.json in run(). Harbor viewer
        # picks it up from there automatically.
        pass


def _prune_artifacts(root: Path, excludes: list[str]) -> None:
    """Remove excluded patterns from a downloaded artifacts directory."""
    for pattern in excludes:
        for match in root.rglob(pattern):
            if match.is_dir():
                shutil.rmtree(match, ignore_errors=True)
            elif match.is_file():
                match.unlink(missing_ok=True)


def _dir_size(path: Path) -> int:
    """Total size in bytes of all files under path."""
    if not path.exists():
        return 0
    return sum(f.stat().st_size for f in path.rglob("*") if f.is_file())
