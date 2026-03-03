"""Harbor adapter for serf — a non-interactive coding agent."""

import json
import os
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
_CONTAINER_STATE_DIR = "/logs/agent/agent-state"

_ARTIFACT_EXCLUDES = [
    ".git", "node_modules", "__pycache__", ".venv",
    "*.pyc", "*.o", "*.so", ".cache",
]
_ARTIFACT_WARN_MB = 100


class SerfAgent(BaseInstalledAgent):
    """Serf agent: headless, non-interactive coding agent."""

    def __init__(self, max_rounds: int = 100, min_result_round: int = 0, reasoning_effort: str = "", enable_reviewer_gate: bool = False, result_tool_name: str = "", plugin_dirs: str = "", system_prompt_append: str = "", *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._max_rounds = max_rounds
        self._min_result_round = min_result_round
        self._reasoning_effort = reasoning_effort
        self._enable_reviewer_gate = enable_reviewer_gate
        self._result_tool_name = result_tool_name
        # Comma-separated host paths to plugin directories.
        self._plugin_dirs = [p.strip() for p in plugin_dirs.split(",") if p.strip()] if plugin_dirs else []
        # Comma-separated host paths to files appended to system prompt.
        self._system_prompt_append = [p.strip() for p in system_prompt_append.split(",") if p.strip()] if system_prompt_append else []

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

        # Upload plugin directories into the container.
        if self._plugin_dirs:
            await environment.exec(command="mkdir -p /installed-agent/plugins")
            for host_path in self._plugin_dirs:
                name = Path(host_path).name
                container_path = f"/installed-agent/plugins/{name}"
                await environment.upload_dir(
                    source_dir=Path(host_path),
                    target_dir=container_path,
                )

        # Upload system-prompt-append files into the container.
        if self._system_prompt_append:
            await environment.exec(command="mkdir -p /installed-agent/prompts")
            for host_path in self._system_prompt_append:
                name = Path(host_path).name
                await environment.upload_file(
                    source_path=Path(host_path),
                    target_path=f"/installed-agent/prompts/{name}",
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

        plugin_flags = ""
        for host_path in self._plugin_dirs:
            name = Path(host_path).name
            plugin_flags += f"--plugin-dir /installed-agent/plugins/{name} "

        append_flags = ""
        for host_path in self._system_prompt_append:
            name = Path(host_path).name
            append_flags += f"--system-prompt-append /installed-agent/prompts/{name} "

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
                    f"{plugin_flags}"
                    f"{append_flags}"
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
            # Download agent session traces from the container, even on timeout.
            local_state_dir = self.logs_dir / "agent-state"
            try:
                await environment.download_dir(_CONTAINER_STATE_DIR, local_state_dir)
                self.logger.info("Downloaded agent traces to %s", local_state_dir)
            except Exception as e:
                self.logger.warning("Could not download agent traces: %s", e)

            # Copy ATIF trajectory to logs_dir root for harbor viewer.
            traj_src = local_state_dir / "trajectory.json"
            if traj_src.exists():
                shutil.copy2(traj_src, self.logs_dir / "trajectory.json")
                self.logger.info("Copied ATIF trajectory to %s", self.logs_dir / "trajectory.json")

            # Populate context now that trajectory is available.
            # (populate_context_post_run was already called by super().run()
            # before traces were downloaded — this second call has the data.)
            self._populate_context(context)

            # Extract agent artifacts from /app (filtered).
            artifacts_dir = self.logs_dir / "artifacts"
            try:
                await environment.download_dir("/app", artifacts_dir)
                _prune_artifacts(artifacts_dir, _ARTIFACT_EXCLUDES)
                total = _dir_size(artifacts_dir)
                if total > _ARTIFACT_WARN_MB * 1024 * 1024:
                    self.logger.warning(
                        "Large artifacts: %dMB in /app (threshold: %dMB)",
                        total // (1024 * 1024), _ARTIFACT_WARN_MB,
                    )
            except Exception as e:
                self.logger.warning("Could not download /app artifacts: %s", e)

    def populate_context_post_run(self, context: AgentContext) -> None:
        # Called by super().run() before traces are downloaded. Actual
        # population happens in _populate_context() from our finally block.
        pass

    def _populate_context(self, context: AgentContext) -> None:
        """Populate harbor context with token usage from ATIF trajectory."""
        traj_path = self.logs_dir / "trajectory.json"
        if not traj_path.exists():
            return

        try:
            traj = json.loads(traj_path.read_text())
        except (json.JSONDecodeError, OSError) as e:
            self.logger.warning("Failed to read ATIF trajectory: %s", e)
            return

        metrics = traj.get("final_metrics", {})
        context.n_input_tokens = metrics.get("total_prompt_tokens", 0)
        context.n_output_tokens = metrics.get("total_completion_tokens", 0)
        context.n_cache_tokens = metrics.get("total_cached_tokens", 0)

        self.logger.info(
            "Serf finished: %d steps, %d prompt tokens, %d completion tokens",
            metrics.get("total_steps", 0),
            context.n_input_tokens,
            context.n_output_tokens,
        )


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
