#!/usr/bin/env python3
"""AutoPoC Test Script for Flock"""
import json, os, sys, time, urllib.request, urllib.error

SERVICE_URL = os.environ.get("SERVICE_URL", sys.argv[1] if len(sys.argv) > 1 else "")
MAX_RETRIES = 5
RETRY_DELAY = 10
results = []

def test_scenario(name, description, method, path, body=None,
                  expected_status=200, expected_content=None, timeout=30):
    url = f"{SERVICE_URL.rstrip('/')}{path}"
    start = time.time()
    for attempt in range(MAX_RETRIES):
        try:
            if body:
                data = json.dumps(body).encode() if isinstance(body, dict) else body.encode()
                req = urllib.request.Request(url, data=data, method=method)
                req.add_header("Content-Type", "application/json")
            else:
                req = urllib.request.Request(url, method=method)
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                status = resp.status
                response_body = resp.read().decode()
                if status == expected_status:
                    if expected_content and expected_content not in response_body:
                        r = {"scenario_name": name, "status": "fail",
                             "output": response_body[:2000],
                             "error_message": f"Expected '{expected_content}' not in response",
                             "duration_seconds": round(time.time()-start, 2)}
                    else:
                        r = {"scenario_name": name, "status": "pass",
                             "output": response_body[:2000], "error_message": None,
                             "duration_seconds": round(time.time()-start, 2)}
                    results.append(r); return r
                elif attempt < MAX_RETRIES - 1:
                    time.sleep(RETRY_DELAY); continue
                else:
                    r = {"scenario_name": name, "status": "fail",
                         "output": response_body[:2000],
                         "error_message": f"Expected {expected_status}, got {status}",
                         "duration_seconds": round(time.time()-start, 2)}
                    results.append(r); return r
        except urllib.error.HTTPError as e:
            # For some endpoints, non-200 might be expected but is still a response
            if e.code == expected_status:
                response_body = e.read().decode() if e.fp else ""
                r = {"scenario_name": name, "status": "pass",
                     "output": response_body[:2000], "error_message": None,
                     "duration_seconds": round(time.time()-start, 2)}
                results.append(r); return r
            if attempt < MAX_RETRIES - 1:
                print(f"  Retry {attempt+1}/{MAX_RETRIES}: HTTP {e.code}", file=sys.stderr)
                time.sleep(RETRY_DELAY)
            else:
                r = {"scenario_name": name, "status": "fail", "output": "",
                     "error_message": f"HTTP {e.code} after {MAX_RETRIES} attempts",
                     "duration_seconds": round(time.time()-start, 2)}
                results.append(r); return r
        except urllib.error.URLError as e:
            if attempt < MAX_RETRIES - 1:
                print(f"  Retry {attempt+1}/{MAX_RETRIES}: {e}", file=sys.stderr)
                time.sleep(RETRY_DELAY)
            else:
                r = {"scenario_name": name, "status": "error", "output": "",
                     "error_message": f"Unreachable after {MAX_RETRIES} attempts: {e}",
                     "duration_seconds": round(time.time()-start, 2)}
                results.append(r); return r
        except Exception as e:
            r = {"scenario_name": name, "status": "error", "output": "",
                 "error_message": str(e),
                 "duration_seconds": round(time.time()-start, 2)}
            results.append(r); return r

# === SCENARIOS ===

# Scenario 1: Health check
print("Testing health-check...", file=sys.stderr)
test_scenario(
    name="health-check",
    description="Verify /healthz endpoint returns 200 OK",
    method="GET",
    path="/healthz",
    expected_status=200
)

# Scenario 2: Admin dashboard
print("Testing admin-dashboard...", file=sys.stderr)
test_scenario(
    name="admin-dashboard",
    description="Verify embedded web dashboard is served at /",
    method="GET",
    path="/",
    expected_status=200,
    expected_content="<html"
)

# Scenario 3: Models API
print("Testing models-api...", file=sys.stderr)
test_scenario(
    name="models-api",
    description="Verify OpenAI-compatible /v1/models endpoint",
    method="GET",
    path="/v1/models",
    expected_status=200
)

# Scenario 4: Prometheus metrics
print("Testing prometheus-metrics...", file=sys.stderr)
test_scenario(
    name="prometheus-metrics",
    description="Verify Prometheus metrics at /metrics",
    method="GET",
    path="/metrics",
    expected_status=200,
    expected_content="# HELP"
)

# === END SCENARIOS ===

print(json.dumps({"results": results}, indent=2))
sys.exit(1 if any(r["status"] in ("fail", "error") for r in results) else 0)
