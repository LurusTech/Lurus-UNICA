#!/usr/bin/env python3
"""Configure the model provider and each UNICA app's prompt in Dify.

This replaces the manual console work that doc/active_task.md recorded as a
blocker: router/internal/bridge/dify_admin.go pushes prompt changes with
`PUT /apps/{id}`, which a real Dify 0.15.3 rejects with 400. The working endpoint
is `POST /console/api/apps/{id}/model-config`, which takes the whole model_config
object rather than a single field — that is why the partial update fails.

The prompt written here deliberately contains no policy numbers. It references
{{facts_context}}, which the router fills from the product line's ontology, so a
policy change never requires touching Dify again.

Usage (inside WSL, with the stack running):
    DIFY_PASSWORD=... DEEPSEEK_API_KEY=... python3 configure_apps.py
"""

import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("DIFY_BASE", "http://localhost:3402").rstrip("/")
EMAIL = os.environ.get("DIFY_EMAIL", "admin@unica.local")
PASSWORD = os.environ.get("DIFY_PASSWORD", "")
API = BASE + "/console/api"

PROVIDER = os.environ.get("DIFY_PROVIDER", "deepseek")
MODEL = os.environ.get("DIFY_MODEL", "deepseek-chat")

# Context variables the router injects. Declaring them as paragraph inputs is
# what makes Dify accept them in the chat-messages `inputs` map; an undeclared
# variable is silently dropped.
CONTEXT_VARS = [
    ("facts_context", "确定性事实"),
    ("experience_context", "历史经验"),
    ("knowledge_context", "参考知识"),
    ("customer_name", "客户标识"),
    ("channel", "渠道"),
    ("product_line", "产品线"),
]

PRE_PROMPT = """你是{{product_line}}的在线客服。用简体中文、简洁专业地回答客户问题。

【本业务确定性事实】
{{facts_context}}

【历史经验】
{{experience_context}}

【参考知识】
{{knowledge_context}}

回答规则：
1. 上述"确定性事实"优先级最高，与其他信息冲突时以它为准，不得改写、换算或推测。
2. 事实中列为"不提供"的服务，客户问及时必须明确告知不支持，不要含糊带过，也不要用行业常识替客户补一个答案。
3. 若某项事实按情形不同（如按商品类别、服务阶段、客户类型分档），回答时必须说明适用情形，不得只给一个数字。
4. 确定性事实中没有的具体数值（价格、参数、库存、个人订单进度），不要编造，请客户提供具体信息或转人工。
5. 每引用一条确定性事实，在该句末尾附加标签 [FACT:属性名=取值]；若该事实分情形，写作 [FACT:情形.属性名=取值]。标签不会展示给客户。
"""


def request(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read().decode()
    except urllib.error.HTTPError as exc:
        raise SystemExit(f"{method} {path} -> HTTP {exc.code}: {exc.read().decode()[:400]}")
    return json.loads(raw) if raw else {}


def login():
    if not PASSWORD:
        raise SystemExit("DIFY_PASSWORD is not set")
    resp = request("POST", "/login", body={"email": EMAIL, "password": PASSWORD})
    token = (resp.get("data") or {}).get("access_token")
    if not token:
        raise SystemExit(f"login failed: {resp}")
    return token


def configure_provider(token):
    api_key = os.environ.get("DEEPSEEK_API_KEY", "")
    if not api_key:
        raise SystemExit("DEEPSEEK_API_KEY is not set")
    request("POST", f"/workspaces/current/model-providers/{PROVIDER}", token=token,
            body={"credentials": {"api_key": api_key}})
    print(f"provider {PROVIDER} configured")


def model_config():
    return {
        "model": {
            "provider": PROVIDER,
            "name": MODEL,
            "mode": "chat",
            # Low temperature: this is a factual assistant, and the ontology is
            # only worth injecting if the model then sticks to it.
            "completion_params": {"temperature": 0.3, "max_tokens": 1024},
        },
        "pre_prompt": PRE_PROMPT,
        "prompt_type": "simple",
        "user_input_form": [
            {"paragraph": {"label": label, "variable": var, "required": False, "default": ""}}
            for var, label in CONTEXT_VARS
        ],
        "opening_statement": "",
        "suggested_questions": [],
        "suggested_questions_after_answer": {"enabled": False},
        "speech_to_text": {"enabled": False},
        "text_to_speech": {"enabled": False},
        "retriever_resource": {"enabled": True},
        "annotation_reply": {"enabled": False},
        "more_like_this": {"enabled": False},
        "sensitive_word_avoidance": {"enabled": False, "type": "", "configs": []},
        "external_data_tools": [],
        "agent_mode": {"enabled": False, "strategy": None, "tools": [], "prompt": None},
        "dataset_configs": {"retrieval_model": "multiple"},
        "file_upload": {"image": {"enabled": False, "number_limits": 3, "detail": "high",
                                  "transfer_methods": ["remote_url", "local_file"]}},
        "chat_prompt_config": {},
        "completion_prompt_config": {},
        "dataset_query_variable": None,
    }


def main():
    token = login()
    configure_provider(token)

    apps_path = os.environ.get("APPS", "/tmp/dify_apps.json")
    with open(apps_path, encoding="utf-8") as fh:
        apps = json.load(fh)

    cfg = model_config()
    for name, binding in apps.items():
        request("POST", f"/apps/{binding['app_id']}/model-config", token=token, body=cfg)
        print(f"{name:<12} model-config updated -> {PROVIDER}/{MODEL}")

    print("\nprompt references {{facts_context}} only; policy numbers stay in the ontology")


if __name__ == "__main__":
    sys.exit(main())
