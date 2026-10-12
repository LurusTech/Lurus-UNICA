"""Tool: POST /judge on the configured gate and surface the verdict.

The tool never rewrites the reply. On a hold it returns the verdict and the
detail; the workflow decides what to do with them — the reference workflow
routes `withheld == true` to a hand-off branch and sends `answer` otherwise.
"""

from collections.abc import Generator
from typing import Any

import requests
from dify_plugin import Tool
from dify_plugin.entities.tool import ToolInvokeMessage


class JudgeTool(Tool):
    def _invoke(self, tool_parameters: dict[str, Any]) -> Generator[ToolInvokeMessage, None, None]:
        creds = self.runtime.credentials
        endpoint = (creds.get("endpoint") or "").rstrip("/")
        headers = {"Content-Type": "application/json"}
        if creds.get("token"):
            headers["Authorization"] = f"Bearer {creds['token']}"

        answer = (tool_parameters.get("answer") or "").strip()
        if not answer:
            # Nothing to judge is a wiring mistake upstream; say so rather
            # than returning a verdict on an empty string.
            yield self.create_json_message({"error": "answer is empty", "withheld": True, "decision": "handoff",
                                            "reason": "empty_answer", "detail": "没有可判定的回复文本"})
            return

        body: dict[str, Any] = {"query": tool_parameters.get("query") or "", "answer": answer}
        grounding = tool_parameters.get("grounding") or ""
        if grounding.strip():
            body["grounding"] = [p for p in grounding.split("\n") if p.strip()]
        for key in ("product_line", "conversation_id"):
            if tool_parameters.get(key):
                body[key] = tool_parameters[key]

        try:
            resp = requests.post(f"{endpoint}/judge", json=body, headers=headers, timeout=10)
        except requests.RequestException as exc:
            # Fail closed: a gate that cannot be reached must not mean "send".
            yield self.create_json_message({"error": f"gate unreachable: {exc}", "withheld": True,
                                            "decision": "handoff", "reason": "gate_unreachable",
                                            "detail": "闸门不可达，按转人工处理"})
            return
        if resp.status_code != 200:
            yield self.create_json_message({"error": f"gate returned {resp.status_code}: {resp.text[:200]}",
                                            "withheld": True, "decision": "handoff", "reason": "gate_error",
                                            "detail": "闸门返回错误，按转人工处理"})
            return

        verdict = resp.json()
        out = {k: verdict.get(k) for k in ("decision", "withheld", "would_withhold", "reason", "detail",
                                          "answer", "evidence_id", "mode", "confidence")}
        yield self.create_json_message(out)
        yield self.create_variable_message("decision", out.get("decision"))
        yield self.create_variable_message("withheld", bool(out.get("withheld")))
        yield self.create_variable_message("would_withhold", bool(out.get("would_withhold")))
        yield self.create_variable_message("answer", out.get("answer") or "")
        yield self.create_variable_message("detail", out.get("detail") or "")
        yield self.create_variable_message("evidence_id", out.get("evidence_id") or "")
        yield self.create_text_message(out.get("answer") or "")
