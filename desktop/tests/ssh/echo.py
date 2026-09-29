import socketserver

class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        data = b''
        while b'\r\n\r\n' not in data:
            chunk = self.request.recv(1024)
            if not chunk: return
            data += chunk
            if len(data) > 4096: return
        body = b'dockyard-fixture-' * 131072
        self.request.sendall(b'HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: ' + str(len(body)).encode() + b'\r\n\r\n' + body)

class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True

Server(('127.0.0.1', 9123), Handler).serve_forever()
