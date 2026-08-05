#!/usr/bin/env python3
"""Seed product_lines with the Dify bindings produced by bootstrap_apps.py.

Names must match the golden evaluation set and the ontology files exactly:
cmd/evalset and cmd/ontology both resolve a product line by name, and a mismatch
surfaces as "product line not found" rather than as anything about naming.

Usage (inside WSL, with the stack running):
    python3 seed_product_lines.py [--apps /tmp/dify_apps.json]
                                  [--base-url http://localhost:3402/v1]
"""

import argparse
import json
import subprocess
import sys

CONTAINER = "unica-dify-postgres"
DB_USER = "dify"
DB_NAME = "unica"


def psql(sql):
    proc = subprocess.run(
        ["docker", "exec", "-i", CONTAINER, "psql", "-U", DB_USER, "-d", DB_NAME,
         "-v", "ON_ERROR_STOP=1"],
        input=sql, capture_output=True, text=True,
    )
    if proc.returncode != 0:
        sys.exit(f"psql failed:\n{proc.stderr.strip()}")
    return proc.stdout


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--apps", default="/tmp/dify_apps.json")
    parser.add_argument("--base-url", default="http://localhost:3402/v1")
    args = parser.parse_args()

    with open(args.apps, encoding="utf-8") as fh:
        apps = json.load(fh)

    statements = []
    for name, binding in apps.items():
        # Upsert on name so re-running after a stack rebuild refreshes the
        # credentials instead of failing or duplicating the row.
        statements.append(
            "INSERT INTO product_lines (name, display_name, dify_agent_id, dify_api_key, dify_base_url, config_json)\n"
            f"VALUES ({quote(name)}, {quote(name)}, {quote(binding['app_id'])}, "
            f"{quote(binding['api_key'])}, {quote(args.base_url)}, '{{}}'::jsonb)\n"
            "ON CONFLICT (name) DO UPDATE SET\n"
            "  dify_agent_id = EXCLUDED.dify_agent_id,\n"
            "  dify_api_key  = EXCLUDED.dify_api_key,\n"
            "  dify_base_url = EXCLUDED.dify_base_url;"
        )

    # product_lines.name has no unique constraint in migration 002, so the
    # upsert above needs one. Added here rather than in a migration because it is
    # a property this tooling relies on, not one the schema promised.
    psql("CREATE UNIQUE INDEX IF NOT EXISTS idx_product_lines_name ON product_lines(name);")
    psql("\n".join(statements))

    print(psql(
        "SELECT name, dify_agent_id, left(dify_api_key, 12) || '...' AS api_key, dify_base_url "
        "FROM product_lines ORDER BY name;"
    ))


if __name__ == "__main__":
    main()
