# LTP Python SDK + WebDAV 参考插件 实现计划（LTP 系列计划 1/5）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 `ltp-plugin` Python SDK（作者只写业务函数即可产出合规的 LTP media-source 插件）与 WebDAV 参考插件，端到端可被 MCP 客户端搜索/取流、可通过 `ltp verify` 契约验证。

**Architecture:** SDK 薄封装官方 `mcp` 包的 FastMCP：作者实现 `MediaSourcePlugin` 子类（async 业务方法 + pydantic 配置模型），SDK 负责工具注册（按能力位）、manifest 生成、配置校验、结构化错误信封、stdio 服务。工具返回统一信封 `{"ok": bool, "data"| "error": ...}`。参考插件仓库演示标准用法。

**Tech Stack:** Python ≥3.11、官方 `mcp` SDK（FastMCP）、pydantic v2、PyYAML、pytest/pytest-asyncio；插件侧另用 webdav4。

## Plan Series（本计划的位置）

1. **本计划**：Python SDK + WebDAV 插件（协议与 SDK 假设验证）
2. `lattice-toolset` Go 库：manifest 解析 + stdio/http toolset 加载器（嵌入 cast/copilot）
3. `lattice-copilot` v1 M1/M2（新仓库，按 copilot spec）
4. cast 四源狗粮重构（内置源迁到 LTP 形态）
5. 容器运行时 + Go SDK + registry CI

## Global Constraints（摘自 LTP spec，逐字生效）

- id 格式 `<plugin>:<native_id>`（如 `webdav:%2Fmedia%2Fa.mkv`），正则 `^[^:]+:.+$`
- `Playable.kind ∈ {url, deep_link}`；临时 url 必须带 `expires_at`，永久 url 允许为 null
- 工具错误必须结构化：`{"ok": false, "error": {"code": str, "message": str, "retriable": bool}}`
- capabilities 词表：`search, stream, save, delete, download-status`；`search`+`stream` 必选
- 工具名与能力位映射：`search→search`、`resolve→stream`、`save→save`、`delete→delete`、`download_status→download-status`；未声明的能力位不注册对应工具
- manifest 顶层 `ltp: "0.1"`；profile 固定 `media-source`
- 只依赖官方 `mcp` SDK 做 MCP 层，不引入其他 MCP 框架
- 代码与测试中不得出现真实凭据（红色红线：灰色集成不进核心仓库）
- 本仓库提交走普通 `git commit`；lattice 仓库钩子自动追加 Signed-off-by，无需手写

## File Structure

SDK 仓库（新仓库 `ltp-plugin-python`，本计划全部 Task 1–6）：

```
pyproject.toml
README.md
src/ltp/__init__.py        # 对外导出：serve, MediaSourcePlugin, models, ToolError
src/ltp/models.py          # MediaItem / Playable / SaveTask / ToolError
src/ltp/manifest.py        # Manifest + manifest_from_plugin
src/ltp/config.py          # load_config
src/ltp/server.py          # build_server：能力位→工具注册 + 信封包装
src/ltp/cli.py             # ltp verify / ltp manifest
src/ltp/__main__.py        # python -m ltp
tests/test_models.py
tests/test_manifest.py
tests/test_config.py
tests/test_server.py
tests/test_verify.py
tests/stdio_e2e.py         # 可复用的 stdio 端到端客户端辅助
tests/fixtures/dummy_plugin.py
```

插件仓库（新仓库 `ltp-plugin-webdav`，Task 7）：

```
pyproject.toml
README.md
manifest.yaml              # 由 ltp manifest 生成后提交
src/webdav_source/__init__.py
tests/test_plugin.py
```

---

### Task 1: SDK 仓库骨架与数据模型

**Files:**
- Create: `pyproject.toml`, `README.md`, `src/ltp/__init__.py`, `src/ltp/models.py`, `src/ltp/__main__.py`
- Test: `tests/test_models.py`

**Interfaces:**
- Produces: `MediaItem(id, title, year, type, poster, source)`、`Playable(kind, url, expires_at, container, note)`、`SaveTask(task_id, status, progress, error)`、`ToolError(code, message, retriable=False)` 含 `.to_dict()`。后续所有任务依赖这些精确签名。

- [ ] **Step 1: 创建仓库与 pyproject**

```bash
mkdir -p ltp-plugin-python/src/ltp ltp-plugin-python/tests/fixtures && cd ltp-plugin-python && git init
```

`pyproject.toml`：

```toml
[project]
name = "ltp-plugin"
version = "0.1.0"
description = "Lattice Toolset Protocol plugin SDK for Python (media-source profile)"
requires-python = ">=3.11"
dependencies = ["mcp>=1.2.0", "pydantic>=2.7", "pyyaml>=6.0"]

[project.optional-dependencies]
dev = ["pytest>=8", "pytest-asyncio>=0.23"]

[project.scripts]
ltp = "ltp.cli:main"

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/ltp"]

[tool.pytest.ini_options]
asyncio_mode = "auto"
```

- [ ] **Step 2: 写失败测试 `tests/test_models.py`**

```python
import pytest
from datetime import datetime, timezone
from pydantic import ValidationError
from ltp.models import MediaItem, Playable, SaveTask, ToolError


def test_media_item_requires_prefixed_id():
    with pytest.raises(ValidationError):
        MediaItem(id="nocolon", title="x")
    item = MediaItem(id="webdav:/a.mkv", title="a")
    assert item.type == "movie" and item.source == ""


def test_playable_url_kind_allows_permanent_url():
    p = Playable(kind="url", url="http://x/a.mkv")
    assert p.expires_at is None  # 永久 url 允许无过期


def test_playable_deep_link_has_no_url():
    p = Playable(kind="deep_link", note="launch only")
    assert p.url is None


def test_save_task_defaults():
    t = SaveTask(task_id="t1", status="queued")
    assert t.progress is None and t.error is None


def test_tool_error_to_dict():
    e = ToolError("upstream_timeout", "115 api timeout", retriable=True)
    assert e.to_dict() == {"code": "upstream_timeout", "message": "115 api timeout", "retriable": True}
```

- [ ] **Step 3: 运行确认失败**

Run: `pip install -e ".[dev]" && pytest tests/test_models.py -v`
Expected: FAIL（`ModuleNotFoundError: No module named 'ltp'`）

- [ ] **Step 4: 最小实现 `src/ltp/models.py`**

```python
from __future__ import annotations
from datetime import datetime
from typing import Literal, Optional
from pydantic import BaseModel, Field

MediaType = Literal["movie", "series", "episode", "audio"]


class MediaItem(BaseModel):
    id: str = Field(pattern=r"^[^:]+:.+$")
    title: str
    year: Optional[int] = None
    type: MediaType = "movie"
    poster: Optional[str] = None
    source: str = ""


class Playable(BaseModel):
    kind: Literal["url", "deep_link"]
    url: Optional[str] = None
    expires_at: Optional[datetime] = None
    container: Optional[str] = None
    note: Optional[str] = None


class SaveTask(BaseModel):
    task_id: str
    status: Literal["queued", "running", "done", "failed"]
    progress: Optional[float] = None
    error: Optional[str] = None


class ToolError(Exception):
    def __init__(self, code: str, message: str, retriable: bool = False):
        super().__init__(message)
        self.code, self.message, self.retriable = code, message, retriable

    def to_dict(self) -> dict:
        return {"code": self.code, "message": self.message, "retriable": self.retriable}
```

`src/ltp/__init__.py`：

```python
from .models import MediaItem, Playable, SaveTask, ToolError
__all__ = ["MediaItem", "Playable", "SaveTask", "ToolError"]
```

`src/ltp/__main__.py`：

```python
from .cli import main
main()
```

`README.md`：三行说明（SDK 用途、`pip install ltp-plugin`、指向 WebDAV 插件仓库为范例）。

- [ ] **Step 5: 运行确认通过**

Run: `pytest tests/test_models.py -v`
Expected: 5 passed

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat(sdk): media-source data models and ToolError"
```

---

### Task 2: Manifest 生成

**Files:**
- Create: `src/ltp/manifest.py`
- Test: `tests/test_manifest.py`, `tests/fixtures/dummy_plugin.py`

**Interfaces:**
- Consumes: 无
- Produces: `Manifest(ltp, name, version, profile, capabilities, runtime, config_schema, auth)` 带 `.to_dict()`/`.to_yaml()`；`manifest_from_plugin(cls) -> Manifest`。约定插件类元数据为类属性：`name: str`、`version: str = "0.1.0"`、`capabilities: list[str]`、`config_model: type[BaseModel] | None = None`。

- [ ] **Step 1: 写 fixture 插件 `tests/fixtures/dummy_plugin.py`**

```python
from pydantic import BaseModel
from ltp.models import MediaItem, Playable


class DummyConfig(BaseModel):
    token: str = "test-token"


class Dummy:
    name = "dummy"
    version = "0.1.0"
    capabilities = ["search", "stream"]
    config_model = DummyConfig

    def __init__(self, config):
        self.config = config

    async def search(self, query, type=None):
        items = [MediaItem(id="dummy:1", title="Alpha"), MediaItem(id="dummy:2", title="Beta")]
        return [i for i in items if query.lower() in i.title.lower()]

    async def resolve(self, id):
        return Playable(kind="url", url="http://example.com/a.mkv")

    touch = __init__  # 保持类体非空兼容
```

- [ ] **Step 2: 写失败测试 `tests/test_manifest.py`**

```python
from ltp.manifest import manifest_from_plugin
from tests.fixtures.dummy_plugin import Dummy


def test_manifest_from_dummy():
    m = manifest_from_plugin(Dummy)
    d = m.to_dict()
    assert d["ltp"] == "0.1" and d["name"] == "dummy" and d["profile"] == "media-source"
    assert d["capabilities"] == ["search", "stream"]
    assert d["auth"] == "bearer"
    assert "token" in d["config_schema"]["properties"]
    assert "ltp: " in m.to_yaml()


def test_manifest_without_config_model():
    class Bare:
        name = "bare"
        version = "0.2.0"
        capabilities = ["search", "stream"]
    d = manifest_from_plugin(Bare).to_dict()
    assert d["config_schema"] is None
```

- [ ] **Step 3: 运行确认失败**

Run: `pytest tests/test_manifest.py -v`
Expected: FAIL（`No module named 'ltp.manifest'`）

- [ ] **Step 4: 实现 `src/ltp/manifest.py`**

```python
from __future__ import annotations
from dataclasses import dataclass, field
import yaml
from pydantic import BaseModel

KNOWN_CAPS = {"search", "stream", "save", "delete", "download-status"}


@dataclass
class Manifest:
    name: str
    version: str
    capabilities: list[str]
    ltp: str = "0.1"
    profile: str = "media-source"
    runtime: dict = field(default_factory=lambda: {"mode": "stdio"})
    config_schema: dict | None = None
    auth: str = "bearer"

    def to_dict(self) -> dict:
        return {
            "ltp": self.ltp, "name": self.name, "version": self.version,
            "profile": self.profile, "capabilities": self.capabilities,
            "runtime": self.runtime, "config_schema": self.config_schema, "auth": self.auth,
        }

    def to_yaml(self) -> str:
        return yaml.safe_dump(self.to_dict(), sort_keys=False, allow_unicode=True)


def manifest_from_plugin(cls) -> Manifest:
    caps = list(getattr(cls, "capabilities", []))
    unknown = set(caps) - KNOWN_CAPS
    if unknown:
        raise ValueError(f"unknown capabilities: {sorted(unknown)}; known: {sorted(KNOWN_CAPS)}")
    model = getattr(cls, "config_model", None)
    return Manifest(
        name=cls.name, version=getattr(cls, "version", "0.1.0"), capabilities=caps,
        config_schema=model.model_json_schema() if model else None,
    )
```

- [ ] **Step 5: 运行确认通过**

Run: `pytest tests/test_manifest.py -v`
Expected: 2 passed

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat(sdk): manifest generation from plugin class"
```

---

### Task 3: 配置加载与校验

**Files:**
- Create: `src/ltp/config.py`
- Test: `tests/test_config.py`

**Interfaces:**
- Consumes: `ToolError`
- Produces: `load_config(path: str | None, model: type[BaseModel] | None) -> BaseModel | None`；config 文件缺失/为空视为 `{}`；校验失败抛 `ToolError("config_invalid", ..., retriable=False)`。

- [ ] **Step 1: 写失败测试 `tests/test_config.py`**

```python
import pytest
from pydantic import BaseModel
from ltp.config import load_config
from ltp.models import ToolError


class Cfg(BaseModel):
    token: str
    depth: int = 2


def test_load_valid(tmp_path):
    f = tmp_path / "config.yaml"
    f.write_text("token: abc\n")
    c = load_config(str(f), Cfg)
    assert c.token == "abc" and c.depth == 2


def test_missing_file_means_empty(tmp_path):
    with pytest.raises(ToolError) as ei:
        load_config(str(tmp_path / "nope.yaml"), Cfg)  # 必填字段缺失
    assert ei.value.code == "config_invalid"


def test_no_model_returns_none():
    assert load_config(None, None) is None
```

- [ ] **Step 2: 运行确认失败**

Run: `pytest tests/test_config.py -v`
Expected: FAIL（`No module named 'ltp.config'`）

- [ ] **Step 3: 实现 `src/ltp/config.py`**

```python
from __future__ import annotations
from pathlib import Path
import yaml
from pydantic import BaseModel, ValidationError
from .models import ToolError


def load_config(path: str | None, model: type[BaseModel] | None):
    if model is None:
        return None
    data = {}
    if path and Path(path).exists():
        data = yaml.safe_load(Path(path).read_text()) or {}
    try:
        return model(**data)
    except ValidationError as e:
        raise ToolError("config_invalid", str(e), retriable=False)
```

- [ ] **Step 4: 运行确认通过**

Run: `pytest tests/test_config.py -v`
Expected: 3 passed

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(sdk): yaml config loading with structured invalid-config error"
```

---

### Task 4: MCP server 装配（能力位→工具注册 + 信封包装）

**Files:**
- Create: `src/ltp/server.py`
- Modify: `src/ltp/__init__.py`（追加导出 `serve`、`MediaSourcePlugin`）
- Create: `src/ltp/base.py`（`MediaSourcePlugin` 基类）
- Test: `tests/test_server.py`

**Interfaces:**
- Consumes: Task 1–3 全部产物
- Produces: `MediaSourcePlugin` 基类（方法签名：`async search(query: str, type: str | None) -> list[MediaItem]`、`async resolve(id: str) -> Playable`、`async save(id: str, dest: str | None) -> SaveTask`、`async delete(id: str) -> None`、`async download_status(task_id: str) -> SaveTask`）；`build_server(plugin) -> FastMCP`；`serve(plugin_cls, config_path=None)`。工具返回信封：`{"ok": True, "items": [...]}` / `{"ok": True, "playable": {...}}` / `{"ok": True, "task": {...}}` / `{"ok": False, "error": {...}}`；`ltp_health` 工具恒注册。

- [ ] **Step 1: 写失败测试 `tests/test_server.py`**

```python
import json
import pytest
from ltp.models import ToolError
from ltp.server import build_server
from tests.fixtures.dummy_plugin import Dummy


def make_plugin():
    return Dummy(Dummy.config_model(token="t"))


async def call(mcp, name, args):
    # 直接调用 FastMCP 注册的底层函数（跳过传输层）
    fn = mcp._tool_manager._tools[name].fn
    return await fn(**args) if _is_coro(fn) else fn(**args)


def _is_coro(fn):
    import inspect
    return inspect.iscoroutinefunction(fn)


async def test_health_and_registered_tools():
    mcp = build_server(make_plugin())
    tools = mcp._tool_manager._tools
    assert {"search", "resolve", "ltp_health"} <= set(tools)
    assert "save" not in tools  # 未声明能力位不注册
    out = await call(mcp, "ltp_health", {})
    assert out["ok"] and out["capabilities"] == ["search", "stream"]


async def test_search_envelope():
    mcp = build_server(make_plugin())
    out = await call(mcp, "search", {"query": "alp"})
    assert out["ok"] and out["items"][0]["id"] == "dummy:1"


async def test_tool_error_envelope():
    class Boom(Dummy):
        async def search(self, query, type=None):
            raise ToolError("upstream", "boom", retriable=True)
    mcp = build_server(Boom(Boom.config_model(token="t")))
    out = await call(mcp, "search", {"query": "x"})
    assert out["ok"] is False and out["error"]["retriable"] is True
```

- [ ] **Step 2: 运行确认失败**

Run: `pytest tests/test_server.py -v`
Expected: FAIL（`No module named 'ltp.server'`）

- [ ] **Step 3: 实现基类 `src/ltp/base.py`**

```python
from __future__ import annotations
from .models import MediaItem, Playable, SaveTask, ToolError


class MediaSourcePlugin:
    """作者继承此类，只实现业务方法。capabilities 决定注册哪些工具。"""
    name: str = ""
    version: str = "0.1.0"
    capabilities: list[str] = []
    config_model = None

    def __init__(self, config):
        self.config = config

    async def search(self, query: str, type: str | None = None) -> list[MediaItem]:
        raise NotImplementedError

    async def resolve(self, id: str) -> Playable:
        raise NotImplementedError

    async def save(self, id: str, dest: str | None = None) -> SaveTask:
        raise ToolError("not_supported", "save not declared/implemented", False)

    async def delete(self, id: str) -> None:
        raise ToolError("not_supported", "delete not declared/implemented", False)

    async def download_status(self, task_id: str) -> SaveTask:
        raise ToolError("not_supported", "download_status not declared/implemented", False)
```

实现 `src/ltp/server.py`：

```python
from __future__ import annotations
from mcp.server.fastmcp import FastMCP
from .base import MediaSourcePlugin
from .config import load_config
from .manifest import manifest_from_plugin
from .models import MediaItem, Playable, SaveTask, ToolError

CONFIG_PATH_ENV = "LTP_CONFIG"


def _ok(payload: dict) -> dict:
    return {"ok": True, **payload}


def build_server(plugin: MediaSourcePlugin) -> FastMCP:
    m = manifest_from_plugin(type(plugin))
    mcp = FastMCP(plugin.name)

    @mcp.tool()
    def ltp_health() -> dict:
        """LTP plugin health and capability report."""
        return _ok({"name": m.name, "profile": m.profile, "capabilities": m.capabilities})

    if "search" in m.capabilities:
        @mcp.tool()
        async def search(query: str, type: str | None = None) -> dict:
            """Search media by title substring. Returns items with LTP-prefixed ids."""
            try:
                items = await plugin.search(query, type)
                return _ok({"items": [i.model_dump(mode="json") for i in items]})
            except ToolError as e:
                return {"ok": False, "error": e.to_dict()}

    if "stream" in m.capabilities:
        @mcp.tool()
        async def resolve(id: str) -> dict:
            """Resolve a MediaItem id into a playable url or deep_link."""
            try:
                p: Playable = await plugin.resolve(id)
                return _ok({"playable": p.model_dump(mode="json")})
            except ToolError as e:
                return {"ok": False, "error": e.to_dict()}

    if "save" in m.capabilities:
        @mcp.tool()
        async def save(id: str, dest: str | None = None) -> dict:
            """Save/download a media item; returns a SaveTask."""
            try:
                t: SaveTask = await plugin.save(id, dest)
                return _ok({"task": t.model_dump(mode="json")})
            except ToolError as e:
                return {"ok": False, "error": e.to_dict()}

    if "delete" in m.capabilities:
        @mcp.tool()
        async def delete(id: str) -> dict:
            """Delete a media item. Consumers must double-confirm with the user."""
            try:
                await plugin.delete(id)
                return _ok({"deleted": id})
            except ToolError as e:
                return {"ok": False, "error": e.to_dict()}

    if "download-status" in m.capabilities:
        @mcp.tool()
        async def download_status(task_id: str) -> dict:
            """Query progress of a save task."""
            try:
                t: SaveTask = await plugin.download_status(task_id)
                return _ok({"task": t.model_dump(mode="json")})
            except ToolError as e:
                return {"ok": False, "error": e.to_dict()}

    return mcp


def serve(plugin_cls: type[MediaSourcePlugin], config_path: str | None = None):
    import os
    config = load_config(config_path or os.environ.get(CONFIG_PATH_ENV, "config.yaml"),
                         plugin_cls.config_model)
    mcp = build_server(plugin_cls(config))
    mcp.run()
```

`src/ltp/__init__.py` 追加：

```python
from .base import MediaSourcePlugin
from .server import serve, build_server
```

- [ ] **Step 4: 运行确认通过**

Run: `pytest tests/test_server.py -v`
Expected: 3 passed

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(sdk): FastMCP server assembly with capability-gated tools and ok/error envelopes"
```

---

### Task 5: stdio 端到端（子进程 MCP 握手 + 真实调用）

**Files:**
- Modify: `src/ltp/cli.py`（先建骨架：`ltp serve --plugin pkg:Cls --config path`）
- Test: `tests/test_stdio_e2e.py`

**Interfaces:**
- Consumes: `serve()`、`ltp.cli.main`
- Produces: CLI 约定 `ltp <serve|verify|manifest> --plugin <pkg:Cls> [--config <path>]`（`--plugin` 为 `importlib` 可解析的 `module:Class`）；`tests/stdio_e2e.py` 的 `call_tool(plugin_spec, config_yaml, name, args) -> dict` 辅助函数，Task 6 复用。

- [ ] **Step 1: 写 CLI 骨架 `src/ltp/cli.py`（verify 下一任务实现）**

```python
from __future__ import annotations
import argparse, importlib, sys


def _load(spec: str):
    mod, _, cls = spec.partition(":")
    return getattr(importlib.import_module(mod), cls)


def main(argv=None):
    p = argparse.ArgumentParser(prog="ltp")
    sub = p.add_subparsers(dest="cmd", required=True)
    for name in ("serve", "verify", "manifest"):
        sp = sub.add_parser(name)
        sp.add_argument("--plugin", required=True)
        sp.add_argument("--config", default=None)
    a = p.parse_args(argv)
    if a.cmd == "serve":
        from .server import serve
        serve(_load(a.plugin), a.config)
    elif a.cmd == "manifest":
        from .manifest import manifest_from_plugin
        print(manifest_from_plugin(_load(a.plugin)).to_yaml())
    else:
        print("verify: not implemented yet", file=sys.stderr)
        return 2
    return 0
```

- [ ] **Step 2: 写失败测试 `tests/test_stdio_e2e.py`**

```python
import json
import sys
import pytest
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client


async def call_tool(plugin_spec: str, config_yaml: str, name: str, args: dict) -> dict:
    import tempfile, os
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as f:
        f.write(config_yaml)
        path = f.name
    params = StdioServerParameters(
        command=sys.executable,
        args=["-m", "ltp", "serve", "--plugin", plugin_spec, "--config", path],
    )
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            listed = await session.list_tools()
            assert name in {t.name for t in listed.tools}
            res = await session.call_tool(name, args)
            return json.loads(res.content[0].text)


async def test_stdio_roundtrip():
    out = await call_tool("tests.fixtures.dummy_plugin:Dummy", "token: t\n", "search", {"query": "alp"})
    assert out["ok"] is True and out["items"][0]["id"] == "dummy:1"
```

- [ ] **Step 3: 运行确认失败**

Run: `pytest tests/test_stdio_e2e.py -v`
Expected: FAIL（子进程 `ltp serve` 报 verify 分支或 argparse 错误之外的问题——若直接通过说明实现提前，检查信封 JSON 是否经 FastMCP 序列化）

- [ ] **Step 4: 运行确认通过**

Run: `pytest tests/test_stdio_e2e.py -v`
Expected: PASS（FastMCP 将 dict 返回值序列化为 JSON 文本）

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(sdk): cli serve entrypoint and stdio end-to-end roundtrip test"
```

---

### Task 6: `ltp verify` 契约验证命令

**Files:**
- Modify: `src/ltp/cli.py`（实现 verify 分支）
- Create: `tests/test_verify.py`

**Interfaces:**
- Consumes: Task 1–5 全部；`tests/stdio_e2e.py:call_tool`
- Produces: `run_verify(plugin_spec: str, config_path: str | None) -> list[tuple[str, str]]`（返回 `(检查名, PASS|FAIL:原因)` 列表）；`ltp verify` 退出码 0=全过 / 1=有 FAIL。检查项：manifest 可生成且 profile 正确；能力位含 search+stream；`search("test")` 信封 ok 且 items 全部通过 `MediaItem` 校验、id 前缀正确；`resolve(items[0].id)` 信封 ok 且 `Playable` 校验通过、kind=url 时 url 非空；kind=url 且 url 非永久（无 expires_at）仅给 WARNING 不判 FAIL。

- [ ] **Step 1: 写失败测试 `tests/test_verify.py`**

```python
from ltp.cli import run_verify


def test_verify_dummy_passes():
    results = run_verify("tests.fixtures.dummy_plugin:Dummy", None)
    failed = [r for r in results if r[1].startswith("FAIL")]
    assert failed == [], failed
    names = [r[0] for r in results]
    assert {"manifest", "capabilities", "search_contract", "resolve_contract"} <= set(names)


def test_verify_reports_bad_id_prefix():
    from ltp.manifest import Manifest
    import ltp.cli as cli
    orig = cli._contract_search  # 注入坏插件：见 Step 3 中 _contract_search 的可替换设计
    # 简化：直接用假信封验证检查逻辑
    bad = [{"id": "nocolon", "title": "x"}]
    fails = cli._check_items(bad, "dummy")
    assert any("FAIL" in r[1] for r in fails)
```

- [ ] **Step 2: 运行确认失败**

Run: `pytest tests/test_verify.py -v`
Expected: FAIL（`ImportError: cannot import name 'run_verify'`）

- [ ] **Step 3: 在 `src/ltp/cli.py` 实现 verify（替换 verify 分支并追加函数）**

```python
def _check_items(items: list[dict], plugin_name: str):
    out = []
    from .models import MediaItem
    for it in items:
        try:
            m = MediaItem.model_validate(it)
            if not m.id.startswith(f"{plugin_name}:"):
                out.append(("item_prefix", f"FAIL: id {m.id} lacks '{plugin_name}:' prefix"))
            else:
                out.append(("item_prefix", f"PASS: {m.id}"))
        except Exception as e:
            out.append(("item_schema", f"FAIL: {e}"))
    return out


def run_verify(plugin_spec: str, config_path: str | None):
    from .config import load_config
    from .manifest import manifest_from_plugin
    results = []
    cls = _load(plugin_spec)
    m = manifest_from_plugin(cls)
    results.append(("manifest", "PASS" if m.profile == "media-source" else "FAIL: wrong profile"))
    caps_ok = {"search", "stream"} <= set(m.capabilities)
    results.append(("capabilities", "PASS" if caps_ok else "FAIL: search+stream required"))
    plugin = cls(load_config(config_path, cls.config_model))
    import asyncio

    async def _run():
        from .server import build_server
        mcp = build_server(plugin)
        import inspect
        s = mcp._tool_manager._tools["search"].fn
        out = await s(query="test", type=None) if inspect.iscoroutinefunction(s) else s(query="test")
        if not out.get("ok"):
            return results.append(("search_contract", f"FAIL: {out.get('error')}"))
        results.append(("search_contract", "PASS"))
        for name, verdict in _check_items(out.get("items", []), m.name):
            results.append((name, verdict))
        r = mcp._tool_manager._tools["resolve"].fn
        first = out["items"][0]["id"]
        pout = await r(id=first) if inspect.iscoroutinefunction(r) else r(id=first)
        if not pout.get("ok"):
            return results.append(("resolve_contract", f"FAIL: {pout.get('error')}"))
        pl = pout["playable"]
        if pl["kind"] == "url" and not pl.get("url"):
            results.append(("resolve_contract", "FAIL: kind=url but url empty"))
        else:
            results.append(("resolve_contract", "PASS"))
            if pl["kind"] == "url" and not pl.get("expires_at"):
                results.append(("expires_at", "WARNING: url without expires_at (ok if permanent)"))
    asyncio.run(_run())
    return results
```

main() 中替换 verify 分支：

```python
    else:
        for name, verdict in run_verify(a.plugin, a.config):
            print(f"{name:20s} {verdict}")
        return 1 if any(v.startswith("FAIL") for _, v in
                        run_verify(a.plugin, a.config)) else 0
```

- [ ] **Step 4: 运行确认通过**

Run: `pytest tests/test_verify.py -v`
Expected: 2 passed；另跑 `ltp verify --plugin tests.fixtures.dummy_plugin:Dummy`（pytest 已装配环境）Expected: 全 PASS + 1 条 WARNING 无关紧要，退出码 0

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(sdk): ltp verify contract checks for media-source profile"
```

---

### Task 7: WebDAV 参考插件（新仓库）+ 端到端验收

**Files（新仓库 `ltp-plugin-webdav`）:**
- Create: `pyproject.toml`, `README.md`, `src/webdav_source/__init__.py`, `manifest.yaml`（生成后提交）
- Test: `tests/test_plugin.py`

**Interfaces:**
- Consumes: PyPI 包 `ltp-plugin`（本仓库 Task 1–6 产物；开发期以路径依赖安装）
- Produces: 可运行的 media-source 插件：能力位 `search, stream`；id 形如 `webdav:%2Fmedia%2Fa.mkv`（路径 URL 编码）；`resolve` 返回 `kind=url` 的 WebDAV 直链，`note` 说明需 basic auth；`expires_at=None`（永久直链）。

- [ ] **Step 1: 建仓库与 pyproject**

```bash
mkdir -p ltp-plugin-webdav/src/webdav_source ltp-plugin-webdav/tests && cd ltp-plugin-webdav && git init
```

`pyproject.toml`：

```toml
[project]
name = "ltp-plugin-webdav"
version = "0.1.0"
description = "LTP media-source plugin: WebDAV library"
requires-python = ">=3.11"
dependencies = ["ltp-plugin", "webdav4>=0.10", "httpx>=0.27"]

[project.optional-dependencies]
dev = ["pytest>=8", "pytest-asyncio>=0.23"]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/webdav_source"]

[tool.pytest.ini_options]
asyncio_mode = "auto"
```

- [ ] **Step 2: 写失败测试 `tests/test_plugin.py`**

```python
import pytest
from pydantic import HttpUrl
from webdav_source import WebDAVSource, WebDAVConfig


def make_plugin(files):
    cfg = WebDAVConfig(url=HttpUrl("https://dav.example.com"), username="u", password="p")
    p = WebDAVSource(cfg)
    p._list_files = lambda: files  # 隔离 I/O：单测不打真实 WebDAV
    return p


async def test_search_filters_by_substring():
    p = make_plugin(["/media/Alpha.mkv", "/media/beta.mp4", "/docs/readme.txt"])
    items = await p.search("alp")
    assert [i.id for i in items] == ["webdav:%2Fmedia%2FAlpha.mkv"]
    assert items[0].title == "Alpha.mkv"


async def test_search_ignores_non_media():
    p = make_plugin(["/docs/readme.txt"])
    assert await p.search("readme") == []


async def test_resolve_returns_permanent_url():
    p = make_plugin(["/media/Alpha.mkv"])
    pl = await p.resolve("webdav:%2Fmedia%2FAlpha.mkv")
    from urllib.parse import unquote
    assert pl.kind == "url" and pl.url == "https://dav.example.com/media/Alpha.mkv"
    assert pl.expires_at is None
```

- [ ] **Step 3: 运行确认失败**

Run: `pip install -e ".[dev]" && pytest tests/test_plugin.py -v`
Expected: FAIL（模块不存在）

- [ ] **Step 4: 实现插件 `src/webdav_source/__init__.py`**

```python
from __future__ import annotations
from urllib.parse import quote
from pydantic import BaseModel, HttpUrl
from ltp import MediaSourcePlugin, MediaItem, Playable

VIDEO_EXTS = {".mkv", ".mp4", ".avi", ".mov", ".flv", ".wmv", ".ts", ".m2ts"}


class WebDAVConfig(BaseModel):
    url: HttpUrl
    username: str = ""
    password: str = ""
    path: str = "/"
    max_depth: int = 2


class WebDAVSource(MediaSourcePlugin):
    name = "webdav"
    version = "0.1.0"
    capabilities = ["search", "stream"]
    config_model = WebDAVConfig

    def _list_files(self) -> list[str]:
        """真实 I/O 隔离点：列出自 config.path 起所有文件路径（测试时被替换）。"""
        from webdav4.fsspec import WebdavFileSystem
        fs = WebdavFileSystem(
            base_url=str(self.config.url),
            auth=(self.config.username, self.config.password) if self.config.username else None,
        )
        return [p for p in fs.find(self.config.path) if fs.isdir(p) is False]

    async def search(self, query: str, type: str | None = None) -> list[MediaItem]:
        import asyncio
        q = query.lower()
        files = await asyncio.to_thread(self._list_files)
        items = []
        for path in files:
            name = path.rsplit("/", 1)[-1]
            dot = name.rfind(".")
            if dot == -1 or name[dot:].lower() not in VIDEO_EXTS:
                continue
            if q not in name.lower():
                continue
            items.append(MediaItem(
                id=f"webdav:{quote(path, safe='')}",
                title=name,
                type="movie",
            ))
        return items

    async def resolve(self, id: str) -> Playable:
        if not id.startswith("webdav:"):
            from ltp import ToolError
            raise ToolError("bad_id", f"id must start with 'webdav:', got {id!r}", False)
        from urllib.parse import unquote
        path = unquote(id.split(":", 1)[1])
        base = str(self.config.url).rstrip("/")
        return Playable(kind="url", url=f"{base}{path}",
                        note=f"basic auth required: {self.config.username}")
```

- [ ] **Step 5: 运行确认通过 + 生成 manifest 提交**

Run: `pytest tests/test_plugin.py -v`
Expected: 3 passed

```bash
ltp manifest --plugin webdav_source:WebDAVSource > manifest.yaml
ltp verify --plugin webdav_source:WebDAVSource   # 无真实服务时 search 可能 FAIL：验收见 Step 6
git add -A && git commit -m "feat: webdav reference plugin for LTP media-source profile"
```

- [ ] **Step 6: 端到端验收（对真实 WebDAV 或本地 dufs 容器）**

```bash
docker run -d --name ltp-dav -p 6065:6065 -v "$PWD/fixtures:/data" sigoden/dufs /data -a u:p@/
printf 'url: http://127.0.0.1:6065\nusername: u\npassword: p\npath: /\n' > config.yaml
mkdir -p fixtures && cp tests/fixtures.mkv fixtures/Alpha.mkv 2>/dev/null || touch fixtures/Alpha.mkv
ltp verify --plugin webdav_source:WebDAVSource --config config.yaml
```

Expected: 全部 PASS（允许 1 条 expires_at WARNING），退出码 0。收尾 `docker rm -f ltp-dav`。

- [ ] **Step 7: README（作者视角的 30 分钟教程要点）+ Commit**

README 必含：继承 `MediaSourcePlugin`、声明 `name/capabilities/config_model`、实现 `search/resolve`、`ltp manifest` 生成清单、`ltp verify` 自检、安装到 copilot 的 stdio 配置行示例：

```yaml
toolsets:
  - name: webdav
    cmd: ["python", "-m", "webdav_source"]   # 插件包内含 __main__.py 调 ltp serve
```

（`src/webdav_source/__main__.py`：`from ltp.server import serve; from . import WebDAVSource; serve(WebDAVSource)`——在本步一并实现。）

```bash
git add -A && git commit -m "docs: plugin authoring quickstart"
```

---

## Self-Review 记录

- **Spec 覆盖**：本计划覆盖 LTP spec §5（media-source 剖面全部工具与数据类型）、§6.1 路径 A（SDK 托管清单中的 MCP/manifest/配置/错误/健康项）、§6.3 的 verify 本地化部分、§6.4 的 stdio 开发形态。容器分发、registry、Go SDK/加载器、四源重构 → 计划 2/4/5；copilot 集成 → 计划 3。
- **占位符扫描**：无 TBD/TODO；所有代码步骤含完整代码；`dufs` 验收容器给出确切镜像与参数。
- **类型一致性**：`ToolError(code, message, retriable)`、信封键 `items/playable/task/error`、`search(query, type=None)`/`resolve(id)` 签名在 Task 1/4/5/6/7 间逐字一致；能力位→工具名映射与 Global Constraints 表一致（`download-status` 连字符为能力位、`download_status` 下划线为工具名）。
