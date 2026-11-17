# Record and replay (cassettes)

Save real upstream responses once, then serve them offline for deterministic agent tests.

```toml
[cassette]
mode = "record"          # "off" (default), "record" or "replay"
dir = "tests/cassette"   # default
ignore_fields = []       # extra request keys to ignore when matching
```

`OPERATORLM_CASSETTE_MODE` and `OPERATORLM_CASSETTE_DIR` override the file (not persisted), and `GET`/`POST /admin/cassette` read and change the settings, so CI can start operatorlm in replay mode with no provider keys.

- **Record**: successful (2xx) responses are stored one JSON file per request, including full streamed bodies. An identical request overwrites its previous recording.
- **Replay**: no provider is contacted. A request that is not in the cassette gets a `404` with `type: cassette_miss` and a message naming the closest recorded request and the fields that differ.
- **Matching**: endpoint, model and stream flag plus the request body with volatile data removed: keys such as `id`, `request_id`, `tool_call_id`, `timestamp`, `created`, `user` (at any depth), UUIDs, ISO timestamps and temp-directory paths inside strings. Multipart audio uploads are hashed by form field names, values and file content (the random boundary is ignored). Other non-JSON bodies are hashed as-is.
- **Privacy**: headers and API keys are never stored, but prompts and responses are kept verbatim. Review a cassette before committing it. This is independent of the audit log, which stays redacted and truncated.
