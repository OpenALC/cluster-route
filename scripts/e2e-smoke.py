"""端到端冒烟: 假上游 + 真实 cluster-route 二进制(CI 与本地共用)。

验证:
1. 路由表改写模型名后转发;
2. 上游无视 identity 返回 gzip 时, 客户端拿到明文且用量/成本被统计;
3. 保存设置后重启, sk-cr 密钥不变(数据完整性);
4. 管理接口拒绝非回环 Host(防 DNS rebinding);
5. 未知 API 路径返回 JSON 404 而非 HTML。
"""
import gzip
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

UPSTREAM_PORT = int(os.environ.get("CR_UPSTREAM_PORT", "38085"))
ROUTER_PORT = int(os.environ.get("CR_ROUTER_PORT", "38086"))
seen = []


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n) or b"{}")
        seen.append({"path": self.path, "model": body.get("model"),
                     "accept_encoding": self.headers.get("Accept-Encoding")})
        payload = json.dumps({
            "id": "x", "object": "chat.completion", "model": body.get("model"),
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1000000, "completion_tokens": 1000000,
                      "total_tokens": 2000000},
        }).encode()
        gz = gzip.compress(payload)  # 故意无视 identity
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Encoding", "gzip")
        self.send_header("Content-Length", str(len(gz)))
        self.end_headers()
        self.wfile.write(gz)


def wait_port(port, timeout=15.0):
    end = time.time() + timeout
    while time.time() < end:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def main():
    exe = sys.argv[1]
    tmp = tempfile.mkdtemp(prefix="cr-e2e-")
    data = os.path.join(tmp, "data")
    results = []

    def check(name, ok):
        results.append((name, bool(ok)))
        print(("PASS  " if ok else "FAIL  ") + name)

    up = ThreadingHTTPServer(("127.0.0.1", UPSTREAM_PORT), Upstream)
    threading.Thread(target=up.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{ROUTER_PORT}"

    def start():
        p = subprocess.Popen([exe, "-data", data, "-port", str(ROUTER_PORT)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if not wait_port(ROUTER_PORT):
            raise RuntimeError("router 未启动")
        return p

    def api(method, path, body=None, host=None):
        d = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=d, method=method)
        req.add_header("Content-Type", "application/json")
        if host:
            req.add_header("Host", host)
        return urllib.request.urlopen(req)

    proc = start()
    try:
        key1 = json.loads(api("GET", "/admin/connection").read())["router_key"]

        # --- 配置 ---
        api("POST", "/admin/providers", {
            "name": "fake", "openai_url": f"http://127.0.0.1:{UPSTREAM_PORT}",
            "enabled": True, "api_key": "sk-up"})
        provider = json.loads(api("GET", "/admin/providers").read())[0]
        api("PUT", "/admin/routes", [{"alias": "my-alias", "provider_id": provider["id"],
                                      "upstream_model": "up-model", "enabled": True}])
        api("POST", "/admin/pricing", {"model": "up-model", "input": 1, "output": 2,
                                       "cache_read": 0, "cache_creation": 0})

        # --- 转发 ---
        req = urllib.request.Request(base + "/v1/chat/completions",
                                     data=json.dumps({"model": "my-alias", "messages": [
                                         {"role": "user", "content": "hi"}]}).encode(),
                                     method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer " + key1)
        resp = urllib.request.urlopen(req)
        body = resp.read()
        check("客户端收到可解析明文", body.startswith(b"{") and b"hi" in body)
        check("Content-Encoding 不透传", resp.headers.get("Content-Encoding") is None)
        check("上游收到改写后模型名", seen and seen[-1]["model"] == "up-model")
        check("上游被要求 identity", seen and seen[-1]["accept_encoding"] == "identity")

        time.sleep(1.5)
        t = json.loads(api("GET", "/admin/overview?range=all").read())["totals"]
        check("gzip 响应用量被解析", t["input_tokens"] == 1000000)
        check("成本按上游模型计价", abs(t["cost"] - 3.0) < 0.01)

        # --- 主机白名单 ---
        try:
            api("GET", "/admin/version", host="evil.example")
            check("非回环 Host 被拒", False)
        except urllib.error.HTTPError as e:
            check("非回环 Host 被拒", e.code == 403)

        try:
            req = urllib.request.Request(base + "/chat/completions", data=b"{}", method="POST")
            req.add_header("Content-Type", "application/json")
            urllib.request.urlopen(req)
            check("未知 POST 路径返回 JSON 404", False)
        except urllib.error.HTTPError as e:
            check("未知 POST 路径返回 JSON 404",
                  e.code == 404 and e.headers.get("Content-Type", "").startswith("application/json"))

        # --- 保存设置 -> 重启 -> 密钥不变 ---
        s = json.loads(api("GET", "/admin/settings").read())
        s["inject"]["enabled"] = True
        s["inject"]["text"] = "ci-smoke"
        api("PUT", "/admin/settings", s)
    finally:
        proc.terminate()
        proc.wait(timeout=15)

    proc2 = start()
    try:
        key2 = json.loads(api("GET", "/admin/connection").read())["router_key"]
        check("保存设置并重启后密钥不变", key1 == key2)
        check("设置已持久化",
              json.loads(api("GET", "/admin/settings").read())["inject"]["text"] == "ci-smoke")
    finally:
        proc2.terminate()
        proc2.wait(timeout=15)
        up.shutdown()

    ok = all(r[1] for r in results)
    print(f"\n{sum(1 for r in results if r[1])}/{len(results)} passed")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
