"""Provider: validates that the configured gate answers /healthz."""

from typing import Any

import requests
from dify_plugin import ToolProvider
from dify_plugin.errors.tool import ToolProviderCredentialValidationError


class UnicaJudgeProvider(ToolProvider):
    def _validate_credentials(self, credentials: dict[str, Any]) -> None:
        endpoint = (credentials.get("endpoint") or "").rstrip("/")
        if not endpoint:
            raise ToolProviderCredentialValidationError("闸门地址不能为空")
        try:
            resp = requests.get(f"{endpoint}/healthz", timeout=5)
        except requests.RequestException as exc:  # network, DNS, refused
            raise ToolProviderCredentialValidationError(f"无法连接闸门：{exc}") from exc
        if resp.status_code != 200:
            raise ToolProviderCredentialValidationError(f"闸门 /healthz 返回 {resp.status_code}")
        body = resp.json()
        if not body.get("ok"):
            raise ToolProviderCredentialValidationError("闸门 /healthz 未报告 ok")
