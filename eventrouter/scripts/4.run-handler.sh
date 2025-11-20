#!/bin/bash


cat <<EOF > echo.py
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get('Content-Length'))
        body = self.rfile.read(length)
        print("=== RECEIVED POST ===")
        print(body.decode('utf-8'))
        print("======================")
        self.send_response(200)
        self.end_headers()

httpd = HTTPServer(("0.0.0.0", 9092), Handler)
print("Listening on :9092")
httpd.serve_forever()
EOF

python3 echo.py

rm echo.py

