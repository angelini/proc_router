Start server:

(note: the server in `script.mjs` is purposefully slow to simulate longer response times)

```bash
$ go run cmd/server/main.go -script script.mjs
```

Run script with version 1:

```bash
$ curl -i -H "Content-Type: application/json" -d '{"version": 1}' 127.0.0.1:5251/__meta/version
```

Get proxy request from version 1:

```bash
$ curl -i 127.0.0.1:5251/
```

Upgrade version and ensure requests get proxied to the latest instance:

```
$ curl -i -H "Content-Type: application/json" -d '{"version": 2}' 127.0.0.1:5251/__meta/version && curl -i 127.0.0.1:5251/
```
