#!/usr/bin/env python3
"""Deterministic Ollama-shaped stub. Replies with whatever is in reply.txt."""
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
REPLY = os.path.join(HERE, "reply.txt")
LASTREQ = os.path.join(HERE, "lastreq.json")


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.startswith("/api/tags"):
            self._json({"models": [{
                "name": "stubmodel:1b",
                "digest": "deadbeef",
                "capabilities": ["completion"],
                "details": {"quantization_level": "Q4_K_M",
                            "parameter_size": "1B",
                            "context_length": 32768},
            }]})
        elif self.path.startswith("/api/version"):
            self._json({"version": "0.0.0-stub"})
        elif self.path.startswith("/v1/models"):
            self._json({"data": [{"id": "stubmodel:1b", "object": "model"}]})
        else:
            self._json({}, 404)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        try:
            with open(LASTREQ, "wb") as f:
                f.write(body)
        except Exception:
            pass
        with open(REPLY) as f:
            reply = f.read()
        self._json({
            "id": "stub", "object": "chat.completion", "model": "stubmodel:1b",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": reply},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
        })


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
