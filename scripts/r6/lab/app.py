"""R6 fixed lab: a deliberately vulnerable, deterministic HTTP app.

It is NOT a benchmark target and never exposed publicly. Scenarios run against
127.0.0.1 only. Findings are intentional so A/B runs can compare *behaviour*
(hypothesis formation, evidence, repeat suppression), not luck.
"""
import json
import os
import sqlite3
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("PORT", "18080"))
BASE = os.path.dirname(os.path.abspath(__file__))

DB = sqlite3.connect(":memory:", check_same_thread=False)
DB.executescript(
    """
    CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT, token TEXT);
    CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT);
    INSERT INTO users VALUES (1,'alice','alice@lab.test','tok-alice');
    INSERT INTO users VALUES (2,'bob','bob@lab.test','tok-bob');
    INSERT INTO notes VALUES (1,'welcome','hello');
    INSERT INTO notes VALUES (2,'secret','internal note');
    """
)
DB.commit()


class Handler(BaseHTTPRequestHandler):
    server_version = "R6Lab/1.0"

    def _send(self, code, body, ctype="text/html; charset=utf-8", headers=None):
        data = body.encode() if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        q = urllib.parse.parse_qs(u.query)
        if u.path == "/":
            return self._send(200, """<html><head><title>R6 Lab</title></head><body>
<h1>R6 Lab</h1>
<a href="/api/users?id=1">user 1</a>
<a href="/admin">admin</a>
<form action="/search" method="get"><input name="q" value="welcome"><button>search</button></form>
</body></html>""")
        if u.path == "/search":
            needle = q.get("q", [""])[0]
            # Intentional SQLi: string-concatenated query.
            try:
                rows = DB.execute(
                    "SELECT id,title FROM notes WHERE title LIKE '%" + needle + "%'"
                ).fetchall()
                return self._send(200, json.dumps(rows), "application/json")
            except sqlite3.Error as exc:
                return self._send(500, f"SQL error: {exc}", "text/plain")
        if u.path == "/api/users":
            # Intentional IDOR: any id is returned without an auth check.
            uid = q.get("id", ["1"])[0]
            row = DB.execute(
                "SELECT id,name,email,token FROM users WHERE id=?", (uid,)
            ).fetchone()
            if not row:
                return self._send(404, "not found", "text/plain")
            return self._send(200, json.dumps(
                {"id": row[0], "name": row[1], "email": row[2], "token": row[3]}
            ), "application/json")
        if u.path == "/files":
            # Intentional path traversal: no base-directory confinement.
            name = q.get("name", ["app.py"])[0]
            try:
                path = os.path.join(BASE, name)
                with open(path, "rb") as fh:
                    return self._send(200, fh.read(), "text/plain; charset=utf-8")
            except OSError:
                return self._send(404, "no such file", "text/plain")
        if u.path == "/admin":
            # Intentional access-control flaw: a debug header grants admin.
            if self.headers.get("X-Debug") == "1":
                return self._send(200, json.dumps({"admin": True, "flag": "R6-ADMIN"}), "application/json")
            return self._send(403, "forbidden", "text/plain")
        if u.path == "/redirect":
            nxt = q.get("next", ["/"])[0]
            return self._send(302, "", headers={"Location": nxt})
        return self._send(404, "not found", "text/plain")

    def do_POST(self):
        u = urllib.parse.urlparse(self.path)
        n = int(self.headers.get("Content-Length", "0") or 0)
        body = self.rfile.read(n).decode(errors="replace") if n else ""
        if u.path == "/login":
            # Intentional default credentials.
            if "username=admin" in body and "password=admin" in body:
                return self._send(200, json.dumps({"session": "admin-session", "role": "admin"}), "application/json")
            return self._send(401, "invalid credentials", "text/plain")
        return self._send(404, "not found", "text/plain")


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
